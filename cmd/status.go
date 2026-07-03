package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/spf13/cobra"
)

var (
	statusWatch   bool
	statusTimeout string
)

var statusCmd = &cobra.Command{
	Use:   "status [job_id]",
	Short: "Check job status (read-only)",
	Long: `Check the status of a TTS job. Does not download audio.

If no job_id is provided, checks the most recent job.
Use --watch to poll until the job reaches a terminal state.`,
	Args: maxArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatus(cmd, args)
	},
}

func init() {
	statusCmd.Flags().BoolVar(&statusWatch, "watch", false, "poll until terminal state or timeout")
	statusCmd.Flags().StringVar(&statusTimeout, "timeout", "", "poll timeout for --watch (default: 10m)")
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	resolved := resolvedCfg
	if resolved == nil {
		return &exitError{code: 1, msg: "config not loaded"}
	}
	if resolved.APIKey == "" {
		return &exitError{code: 2, msg: "no API key configured. Run: ttsbuddy config set key <your-key>"}
	}

	// Determine job ID
	var jobID string
	if len(args) > 0 {
		jobID = args[0]
	} else {
		lj, ljErr := config.LoadLastJob()
		if ljErr != nil {
			return &exitError{code: 1, msg: fmt.Sprintf("reading last job: %v", ljErr)}
		}
		if lj == nil {
			return &exitError{code: 2, msg: "no job ID provided and no recent job found. Usage: ttsbuddy status <job_id>"}
		}
		jobID = lj.JobID
		stderrMsg("Using last job: %s\n", jobID)
	}

	client := api.NewClient(resolved.APIURL, resolved.APIKey, Version)

	// Single GET (default)
	if !statusWatch {
		return statusOnce(client, jobID, resolved)
	}

	// --watch: poll until terminal
	return statusPoll(client, jobID, resolved)
}

func statusOnce(client *api.Client, jobID string, resolved *config.ResolvedConfig) error {
	resp, _, err := client.GetStatus(context.Background(), jobID)
	if err != nil {
		return handleStatusError(err, jobID)
	}
	return renderStatus(resp, jobID, resolved)
}

func statusPoll(client *api.Client, jobID string, resolved *config.ResolvedConfig) error {
	timeout := resolved.PollTimeout
	if statusTimeout != "" {
		timeout = statusTimeout
	}

	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return &exitError{code: 2, msg: fmt.Sprintf("invalid timeout value: %s (use Go duration syntax like 30s, 2m, 10m)", timeout)}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	deadline := time.Now().Add(dur)
	delay := 3 * time.Second

	for {
		if time.Now().After(deadline) {
			return &exitError{code: 1, msg: fmt.Sprintf("polling timed out after %s", timeout)}
		}

		resp, pollStatus, err := client.GetStatus(ctx, jobID)
		if err != nil {
			if isPermanentError(err, pollStatus) {
				return handleStatusError(err, jobID)
			}
			stderrMsg("Status check error: %v, retrying...\n", err)
			delay = minDuration(delay*3/2, 15*time.Second)
			select {
			case <-ctx.Done():
				fmt.Fprintf(os.Stderr, "\nInterrupted. Resume with: ttsbuddy status %s --watch\n", jobID)
				os.Exit(130)
			case <-time.After(delay):
			}
			continue
		}

		switch resp.Status {
		case "completed", "failed", "expired":
			return renderStatus(resp, jobID, resolved)
		case "processing":
			elapsed := time.Since(deadline.Add(-dur))
			stderrMsg("%s elapsed\n", renderProgress(resp, elapsed))
			if resp.RetryAfterSeconds != nil {
				delay = time.Duration(*resp.RetryAfterSeconds) * time.Second
			} else {
				delay = minDuration(delay*3/2, 15*time.Second)
			}
		default:
			return &exitError{code: 1, msg: fmt.Sprintf("unexpected job status %q from API. Job ID: %s", resp.Status, jobID)}
		}

		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "\nInterrupted. Resume with: ttsbuddy status %s --watch\n", jobID)
			os.Exit(130)
		case <-time.After(delay):
		}
	}
}

func renderStatus(resp *api.TTSResponse, jobID string, resolved *config.ResolvedConfig) error {
	if resp.Status == "completed" {
		if err := validateCompletedAudioURL(resp, resolved); err != nil {
			return err
		}
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	switch resp.Status {
	case "completed":
		fmt.Fprintf(os.Stderr, "Job %s: completed\n", jobID)
		if resp.AudioURL != "" {
			fmt.Fprintf(os.Stderr, "Audio URL: %s\n", resp.AudioURL)
		}
		if resp.Audio != nil {
			if resp.Audio.DurationSeconds != nil {
				fmt.Fprintf(os.Stderr, "Duration: %.1fs\n", *resp.Audio.DurationSeconds)
			}
			if resp.Audio.ExpiresAt != "" {
				fmt.Fprintf(os.Stderr, "Expires: %s\n", resp.Audio.ExpiresAt)
			}
		}
		renderCompletionSummary(resp, 0)
		if resp.Billing != nil && resp.Billing.MonthlyMinutesUsed != nil {
			fmt.Fprintf(os.Stderr, "Usage: %.1f min used", *resp.Billing.MonthlyMinutesUsed)
			if resp.Billing.MonthlyMinutesLimit != nil {
				fmt.Fprintf(os.Stderr, " / %.0f min limit", *resp.Billing.MonthlyMinutesLimit)
			}
			fmt.Fprintln(os.Stderr)
		}
		return nil

	case "processing":
		fmt.Fprintf(os.Stderr, "Job %s: still processing\n", jobID)
		fmt.Fprintf(os.Stderr, "Check again with: ttsbuddy status %s\n", jobID)
		fmt.Fprintf(os.Stderr, "Or poll until done: ttsbuddy status %s --watch\n", jobID)
		return nil

	case "failed":
		msg := "TTS generation failed"
		if resp.Error != nil {
			msg = resp.Error.Message
		}
		return &exitError{code: 1, msg: fmt.Sprintf("Job %s: failed — %s", jobID, msg)}

	case "expired":
		return &exitError{code: 1, msg: fmt.Sprintf("Job %s: audio expired and deleted. Submit a new request.", jobID)}

	default:
		return &exitError{code: 1, msg: fmt.Sprintf("Job %s: unknown status %q", jobID, resp.Status)}
	}
}

func handleStatusError(err error, jobID string) error {
	var apiErr *api.APIResponseError
	if isAPIErr(err, &apiErr) {
		if apiErr.ErrorCode() == api.ErrNotFound {
			return &exitError{code: 1, msg: fmt.Sprintf("job not found: %s", jobID)}
		}
		return handleAPIError(err, apiErr.StatusCode)
	}
	return &exitError{code: 1, msg: fmt.Sprintf("checking status: %v", err)}
}

func isAPIErr(err error, target **api.APIResponseError) bool {
	return err != nil && asAPIErr(err, target)
}

func asAPIErr(err error, target **api.APIResponseError) bool {
	type unwrapper interface{ Unwrap() error }
	if e, ok := err.(*api.APIResponseError); ok {
		*target = e
		return true
	}
	if u, ok := err.(unwrapper); ok {
		return asAPIErr(u.Unwrap(), target)
	}
	return false
}
