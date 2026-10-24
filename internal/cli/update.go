package cli

import (
	"encoding/json"
	"fmt"
	"github.com/procly/procly/internal/updater"
	"github.com/spf13/cobra"
	"os"
)

func updateCommand(version string) *cobra.Command {
	var check, yes, status bool
	c := &cobra.Command{Use: "update", Short: "Check and install a signed official release", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if status {
			exe, e := os.Executable()
			if e != nil {
				return e
			}
			b, e := os.ReadFile(updater.ResultPath(exe))
			if e != nil {
				return fmt.Errorf("no update result is available: %w", e)
			}
			var result map[string]any
			if e = json.Unmarshal(b, &result); e != nil {
				return e
			}
			fmt.Fprintln(c.OutOrStdout(), string(b))
			if installed, _ := result["installed"].(bool); !installed {
				return fmt.Errorf("update did not install successfully")
			}
			return nil
		}
		client, e := updater.New()
		if e != nil {
			return e
		}
		release, e := client.Latest(c.Context())
		if e != nil {
			return e
		}
		newer, e := updater.Newer(version, release.Tag)
		if e != nil {
			return e
		}
		fmt.Fprintf(c.OutOrStdout(), "\n  procly_ / updater\n\n  Current     %s\n  Available   %s\n\n", version, release.Tag)
		if !newer {
			fmt.Fprintln(c.OutOrStdout(), "You're up to date.")
			return nil
		}
		fmt.Fprintln(c.OutOrStdout(), safeText(release.Notes))
		if check {
			return nil
		}
		if !yes {
			ok, e := confirm(c, "Install this release?")
			if e != nil {
				return e
			}
			if !ok {
				return nil
			}
		}
		last := -1
		dir, exe, e := client.Prepare(c.Context(), release, func(done, total int64) {
			percent := int(done * 100 / total)
			if percent != last {
				fmt.Fprintf(c.ErrOrStderr(), "\rDownloading  %3d%%", percent)
				last = percent
			}
		})
		fmt.Fprintln(c.ErrOrStderr())
		if e != nil {
			return e
		}
		defer os.RemoveAll(dir)
		fmt.Fprintln(c.OutOrStdout(), "Signature and checksum verified.")
		result, e := updater.Stage(exe, release.Tag)
		if e != nil {
			return e
		}
		fmt.Fprintf(c.OutOrStdout(), "Update staged. Installation finishes after this command exits.\nRun procly update --status to verify the result.\nResult: %s\n", result)
		return nil
	}}
	c.Flags().BoolVar(&check, "check", false, "Check without downloading")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "Approve installation")
	c.Flags().BoolVar(&status, "status", false, "Read the validated installation result")
	return c
}
