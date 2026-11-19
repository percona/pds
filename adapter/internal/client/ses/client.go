// Package ses sends one plain text email through Amazon SES.
//
// One call is made, SESv2 SendEmail. Credentials come from the default AWS
// chain, which on an EC2 host is the instance role; no key is ever read from
// this adapter's configuration. An error carries the SES status and message,
// never the email.
package ses

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
)

// Email is the message this adapter sends. Only the fields it uses exist.
type Email struct {
	From    string
	To      []string
	Subject string
	Text    string
}

// Options configures a Client.
type Options struct {
	// Region is the SES region the sender identity is verified in.
	Region string
	// BaseEndpoint overrides the SES endpoint, for tests.
	BaseEndpoint string
	// Credentials overrides the default AWS chain, for tests.
	Credentials aws.CredentialsProvider
	// MaxAttempts caps SDK attempts. Zero keeps the SDK default.
	MaxAttempts int
}

// Client sends email through one SES region.
type Client struct {
	api *sesv2.Client
}

// New loads the default AWS configuration for the region and returns a client.
func New(ctx context.Context, opts Options) (*Client, error) {
	region := strings.TrimSpace(opts.Region)
	if region == "" {
		return nil, errors.New("ses: region must not be blank")
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if opts.Credentials != nil {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(opts.Credentials))
	}
	if opts.MaxAttempts > 0 {
		loadOpts = append(loadOpts, awsconfig.WithRetryMaxAttempts(opts.MaxAttempts))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("ses: load AWS configuration: %w", err)
	}

	api := sesv2.NewFromConfig(cfg, func(o *sesv2.Options) {
		if opts.BaseEndpoint != "" {
			o.BaseEndpoint = aws.String(opts.BaseEndpoint)
		}
	})

	return &Client{api: api}, nil
}

// Send hands one email to SES.
func (c *Client) Send(ctx context.Context, email Email) error {
	_, err := c.api.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(email.From),
		Destination:      &types.Destination{ToAddresses: email.To},
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: &types.Content{Data: aws.String(email.Subject), Charset: aws.String("UTF-8")},
			Body:    &types.Body{Text: &types.Content{Data: aws.String(email.Text), Charset: aws.String("UTF-8")}},
		}},
	})
	if err != nil {
		return fmt.Errorf("ses: send email: %w", describe(err))
	}

	return nil
}

// describe reduces an SDK error to its status or API code and message, so the
// log line never holds the request.
func describe(err error) error {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return fmt.Errorf("%s: %s", apiErr.ErrorCode(), apiErr.ErrorMessage())
	}

	return err
}
