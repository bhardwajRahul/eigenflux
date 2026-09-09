package cmd

import (
	"fmt"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var agentInstallRefCmd = &cobra.Command{
	Use:    "install-ref",
	Short:  "Save the first install referral for this Home and server",
	Hidden: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ref, _ := cmd.Flags().GetString("ref")
		endpoint, _ := cmd.Flags().GetString("endpoint")
		if ref == "" || endpoint == "" {
			return fmt.Errorf("--ref and --endpoint are required")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		server, err := cfg.GetActive(serverFlag)
		if err != nil {
			return err
		}
		record, saved, err := auth.RememberInstallRef(*server, endpoint, ref)
		if err != nil {
			return err
		}
		result := map[string]interface{}{"saved": saved, "server": server.Name}
		if record != nil {
			result["ref"] = record.Ref
		} else {
			result["reason"] = "existing_identity"
		}
		output.PrintData(result, resolveFormat())
		return nil
	},
}

func init() {
	agentInstallRefCmd.Flags().String("ref", "", "referral issued by the install page")
	agentInstallRefCmd.Flags().String("endpoint", "", "website endpoint that issued the referral")
	agentV2Cmd.AddCommand(agentInstallRefCmd)
}
