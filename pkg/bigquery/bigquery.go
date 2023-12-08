package bigquery

import (
	"cloud.google.com/go/bigquery"
	"context"
	"errors"
	"fmt"
	"google.golang.org/api/option"
	"k8s.io/klog/v2"
)

type Client struct {
	*bigquery.Client
}

func NewBigQueryClient(project, credentialsFile string) (*Client, error) {
	bc, err := bigquery.NewClient(context.Background(), project, option.WithCredentialsFile(credentialsFile))
	if err != nil {
		return nil, fmt.Errorf("unable to create BigQuery client: %v", err)
	}
	return &Client{bc}, nil
}

func (c Client) WriteRows(ctx context.Context, dataset, table string, rows interface{}) error {
	inserter := c.Dataset(dataset).Table(table).Inserter()
	err := inserter.Put(ctx, rows)
	if err != nil {
		var multiErr bigquery.PutMultiError
		if errors.As(err, &multiErr) {
			for _, putErr := range multiErr {
				klog.Errorf("failed to insert row %d with err: %v \n", putErr.RowIndex, putErr.Error())
			}
		}
		return err
	}

	return nil
}
