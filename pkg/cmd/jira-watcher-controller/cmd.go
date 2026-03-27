package jira_watcher_controller

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/openshift/ci-search/jira"
	"github.com/openshift/ci-search/pkg/bigquery"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/pkg/version"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"sigs.k8s.io/prow/pkg/flagutil"
	jiraClient "sigs.k8s.io/prow/pkg/jira"
)

type Options struct {
	controllerContext *controllercmd.ControllerContext

	DryRun bool

	// Jira Options
	jira                flagutil.JiraOptions
	JiraSearch          string
	ShowPrivateMessages bool

	// BigQuery Options
	GoogleProjectID                    string
	GoogleServiceAccountCredentialFile string
	BigQueryRefreshInterval            time.Duration
}

func NewJiraWatcherControllerCommand(name string) *cobra.Command {
	o := &Options{
		BigQueryRefreshInterval: 1 * time.Minute,
		// JiraSearch: "filter=105522", // CI Search - 30 Days
		// JiraSearch: "filter=105121", // CI Search - 60 Days
		// JiraSearch: "filter=105523", // CI Search - 90 Days
		// TODO: 3/30/2026 - Remove in roughly 90 days (approx 7/1/2026)
		JiraSearch: "filter=106793", // CI Search - 5 Days
	}

	ccc := controllercmd.NewControllerCommandConfig("jira-watcher-controller", version.Get(), func(ctx context.Context, controllerContext *controllercmd.ControllerContext) error {
		o.controllerContext = controllerContext

		err := o.Validate(ctx)
		if err != nil {
			return err
		}

		err = o.Run(ctx)
		if err != nil {
			return err
		}

		return nil
	}, clock.RealClock{})
	ccc.DisableLeaderElection = true

	cmd := ccc.NewCommandWithContext(context.Background())
	cmd.Use = name
	cmd.Short = "Start the JIRA Watcher Controller"

	o.AddFlags(cmd.Flags())

	return cmd
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.JiraSearch, "jira-search", o.JiraSearch, "A JQL query to search for issues to index.")
	fs.BoolVar(&o.ShowPrivateMessages, "show-private-messages", o.ShowPrivateMessages, "Display Jira comments that are flagged as private.")
	fs.StringVar(&o.GoogleProjectID, "google-project-id", os.Getenv("GOOGLE_PROJECT_ID"), "Google project name.")
	fs.StringVar(&o.GoogleServiceAccountCredentialFile, "google-service-account-credential-file", os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"), "location of a credential file described by https://cloud.google.com/docs/authentication/production")
	fs.DurationVar(&o.BigQueryRefreshInterval, "bigquery-refresh-interval", o.BigQueryRefreshInterval, "How often to push comments into BigQuery. Defaults to 15 minutes.")
	fs.BoolVar(&o.DryRun, "dry-run", o.DryRun, "Perform no actions.")

	goFlagSet := flag.NewFlagSet("prowflags", flag.ContinueOnError)
	o.jira.AddFlags(goFlagSet)
	fs.AddGoFlagSet(goFlagSet)
}

func (o *Options) Validate(_ context.Context) error {
	err := o.jira.Validate(false)
	if err != nil {
		return err
	}
	if len(o.JiraSearch) == 0 {
		return errors.New("--jira-search flag must be set")
	}
	if len(o.GoogleProjectID) == 0 {
		return errors.New("--google-project-id flag must be set")
	}
	if len(o.GoogleServiceAccountCredentialFile) == 0 {
		return errors.New("--google-service-account-credential-file flag must be set")
	}
	return nil
}

func (o *Options) Run(ctx context.Context) error {
	if len(o.JiraSearch) == 0 {
		return fmt.Errorf("--jira-search is required")
	}
	var err error
	var jc jiraClient.Client
	if jc, err = o.jira.Client(); err != nil {
		return fmt.Errorf("unable to create jira client: %v", err)
	}
	c := &jira.Client{
		Client: jc,
	}
	jiraInformer := jira.NewInformer(
		c,
		15*time.Minute, // Time before watcher starts
		2*time.Hour,    // How often to resync from jira
		0,              // Never resync items already in store
		func(metav1.ListOptions) jira.SearchIssuesArgs {
			return jira.SearchIssuesArgs{
				Jql: o.JiraSearch,
			}
		},
		jira.FilterPrivateIssues,
	)
	jiraLister := jira.NewIssueLister(jiraInformer.GetIndexer())

	bqc, err := bigquery.NewBigQueryClient(o.GoogleProjectID, o.GoogleServiceAccountCredentialFile)
	if err != nil {
		klog.Fatalf("Unable to configure bigquery client: %v", err)
	}

	jiraWatcherController, err := NewJiraWatcherController(c, jiraInformer, jiraLister, o.ShowPrivateMessages, bqc, o.DryRun)
	if err != nil {
		return err
	}

	go jiraInformer.Run(ctx.Done())
	go jiraWatcherController.RunWorkers(ctx, 1)

	<-ctx.Done()

	return nil
}
