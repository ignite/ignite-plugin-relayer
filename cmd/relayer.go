package cmd

import (
	"github.com/ignite/cli/ignite/pkg/cliui"
	"github.com/ignite/cli/ignite/pkg/cosmosaccount"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"
)

const (
	flagKeyringBackend = "keyring-backend"
	flagKeyringDir     = "keyring-dir"
)

// NewRelayer creates a new relayer command that holds some other sub commands
func NewRelayer() *cobra.Command {
	c := &cobra.Command{
		Use:     "relayer [command]",
		Aliases: []string{"n"},
		Short:   "Connect blockchains with an IBC relayer",
		Long:    ``,
	}

	c.AddCommand(
		NewRelayerConfigure(),
		NewRelayerConnect(),
	)

	return c
}

func handleRelayerAccountErr(err error) error {
	var accountErr *cosmosaccount.AccountDoesNotExistError
	if !errors.As(err, &accountErr) {
		return err
	}

	return errors.Wrap(accountErr, `make sure to create or import your account through "ignite account" commands`)
}

func flagSetKeyringBackend() *flag.FlagSet {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	fs.String(flagKeyringBackend, string(cosmosaccount.KeyringTest), "keyring backend to store your account keys")
	return fs
}

func getKeyringBackend(cmd *cobra.Command) cosmosaccount.KeyringBackend {
	backend, _ := cmd.Flags().GetString(flagKeyringBackend)
	return cosmosaccount.KeyringBackend(backend)
}

func flagSetKeyringDir() *flag.FlagSet {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	fs.String(flagKeyringDir, cosmosaccount.KeyringHome, "accounts keyring directory")
	return fs
}

func getKeyringDir(cmd *cobra.Command) string {
	keyringDir, _ := cmd.Flags().GetString(flagKeyringDir)
	return keyringDir
}

func printSection(session *cliui.Session, title string) error {
	return session.Printf("------\n%s\n------\n\n", title)
}
