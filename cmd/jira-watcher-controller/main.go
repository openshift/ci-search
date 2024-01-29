package main

import (
	jirawatcher "github.com/openshift/ci-search/pkg/cmd/jira-watcher-controller"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

func main() {
	command := NewJiraWatcherControllerCommand()
	code := cli.Run(command)
	os.Exit(code)
}

func NewJiraWatcherControllerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jira-watcher-controller",
		Short: "CRT Jira Watcher Controller",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}

	cmd.AddCommand(jirawatcher.NewJiraWatcherControllerCommand("start"))
	return cmd
}
