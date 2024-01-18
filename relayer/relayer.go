package relayer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosaccount"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/ignite/cli/v28/ignite/pkg/ctxticker"
	tsrelayer "github.com/ignite/cli/v28/ignite/pkg/nodetime/programs/ts-relayer"
	"github.com/ignite/cli/v28/ignite/pkg/xurl"
	"golang.org/x/sync/errgroup"

	"github.com/ignite/ignite-plugin-relayer/relayer/config"
)

const (
	algoSecp256k1       = "secp256k1"
	ibcSetupGas   int64 = 2256000
	relayDuration       = time.Second * 5
)

// ErrLinkedPath indicates that an IBC path is already liked.
var ErrLinkedPath = errors.New("path already linked")

// Relayer is an IBC relayer.
type Relayer struct {
	ca cosmosaccount.Registry
}

// New creates a new IBC relayer and uses ca to access accounts.
func New(ca cosmosaccount.Registry) Relayer {
	return Relayer{
		ca: ca,
	}
}

// LinkPaths links all chains that has a path from config file to each other.
// paths are optional and acts as a filter to only link some chains.
// calling Link multiple times for the same paths does not have any side effects.
func (r Relayer) LinkPaths(
	ctx context.Context,
	pathIDs ...string,
) error {
	cfg, err := config.Get()
	if err != nil {
		return err
	}

	for _, id := range pathIDs {
		cfg, err = r.Link(ctx, cfg, id)
		if err != nil {
			// Continue with next path when current one is already linked
			if errors.Is(err, ErrLinkedPath) {
				continue
			}
			return err
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
	}
	return nil
}

// Link links chain path to each other.
func (r Relayer) Link(
	ctx context.Context,
	cfg config.Config,
	pathID string,
) (config.Config, error) {
	path, err := cfg.PathByID(pathID)
	if err != nil {
		return cfg, err
	}

	if path.Src.ChannelID != "" {
		return cfg, fmt.Errorf("%w: %s", ErrLinkedPath, path.ID)
	}

	if path, err = r.call(ctx, cfg, path, "link"); err != nil {
		return cfg, err
	}

	return cfg, cfg.UpdatePath(path)
}

// StartPaths relays packets for linked paths from config file until ctx is canceled.
func (r Relayer) StartPaths(ctx context.Context, pathIDs ...string) error {
	cfg, err := config.Get()
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)

	var m sync.Mutex // protects config.Path.
	for _, id := range pathIDs {
		id := id
		g.Go(func() error {
			return r.Start(ctx, cfg, id, func(path config.Config) error {
				m.Lock()
				defer m.Unlock()
				return config.Save(cfg)
			})
		})
	}
	return g.Wait()
}

// Start relays packets for linked path until ctx is canceled.
func (r Relayer) Start(
	ctx context.Context,
	cfg config.Config,
	pathID string,
	postExecute func(path config.Config) error,
) error {
	return ctxticker.DoNow(ctx, relayDuration, func() error {
		path, err := cfg.PathByID(pathID)
		if err != nil {
			return err
		}
		path, err = r.call(ctx, cfg, path, "start")
		if err != nil {
			return err
		}
		if err := cfg.UpdatePath(path); err != nil {
			return err
		}
		if postExecute != nil {
			return postExecute(cfg)
		}
		return nil
	})
}

func (r Relayer) call(
	ctx context.Context,
	cfg config.Config,
	path config.Path,
	action string,
) (
	reply config.Path, err error,
) {
	srcChain, srcKey, err := r.prepare(ctx, cfg, path.Src.ChainID)
	if err != nil {
		return config.Path{}, err
	}

	dstChain, dstKey, err := r.prepare(ctx, cfg, path.Dst.ChainID)
	if err != nil {
		return config.Path{}, err
	}

	args := []interface{}{
		path,
		srcChain,
		dstChain,
		srcKey,
		dstKey,
	}
	return reply, tsrelayer.Call(ctx, action, args, &reply)
}

func (r Relayer) prepare(ctx context.Context, cfg config.Config, chainID string) (
	chain config.Chain, privKey string, err error,
) {
	chain, err = cfg.ChainByID(chainID)
	if err != nil {
		return config.Chain{}, "", err
	}

	coins, err := r.balance(ctx, chain.RPCAddress, chain.Account, chain.AddressPrefix)
	if err != nil {
		return config.Chain{}, "", err
	}

	gasPrice, err := sdk.ParseCoinNormalized(chain.GasPrice)
	if err != nil {
		return config.Chain{}, "", err
	}

	account, err := r.ca.GetByName(chain.Account)
	if err != nil {
		return config.Chain{}, "", err
	}

	addr, err := account.Address(chain.AddressPrefix)
	if err != nil {
		return config.Chain{}, "", err
	}

	errMissingBalance := fmt.Errorf(`account "%s(%s)" on %q chain does not have enough balances`,
		addr,
		chain.Account,
		chain.ID,
	)

	if len(coins) == 0 {
		return config.Chain{}, "", errMissingBalance
	}

	for _, coin := range coins {
		if gasPrice.Denom != coin.Denom {
			continue
		}

		if gasPrice.Amount.Int64()*ibcSetupGas > coin.Amount.Int64() {
			return config.Chain{}, "", errMissingBalance
		}
	}

	// Get the key in ASCII armored format
	passphrase := ""
	key, err := r.ca.Export(chain.Account, passphrase)
	if err != nil {
		return config.Chain{}, "", err
	}

	// Unarmor the key to be able to read it as bytes
	priv, algo, err := crypto.UnarmorDecryptPrivKey(key, passphrase)
	if err != nil {
		return config.Chain{}, "", err
	}

	// Check the algorithm because the TS relayer expects a secp256k1 private key
	if algo != algoSecp256k1 {
		return config.Chain{}, "", fmt.Errorf("private key algorithm must be secp256k1 instead of %s", algo)
	}

	return chain, hex.EncodeToString(priv.Bytes()), nil
}

func (r Relayer) balance(ctx context.Context, rpcAddress, account, addressPrefix string) (sdk.Coins, error) {
	client, err := cosmosclient.New(ctx, cosmosclient.WithNodeAddress(rpcAddress))
	if err != nil {
		return nil, err
	}

	acc, err := r.ca.GetByName(account)
	if err != nil {
		return nil, err
	}

	addr, err := acc.Address(addressPrefix)
	if err != nil {
		return nil, err
	}

	queryClient := banktypes.NewQueryClient(client.Context())
	res, err := queryClient.AllBalances(ctx, &banktypes.QueryAllBalancesRequest{Address: addr})
	if err != nil {
		return nil, err
	}

	return res.Balances, nil
}

// GetPath returns a path by its id.
func (r Relayer) GetPath(_ context.Context, id string) (config.Path, error) {
	cfg, err := config.Get()
	if err != nil {
		return config.Path{}, err
	}

	return cfg.PathByID(id)
}

// ListPaths list all the paths.
func (r Relayer) ListPaths(_ context.Context) ([]config.Path, error) {
	cfg, err := config.Get()
	if err != nil {
		return nil, err
	}

	return cfg.Paths, nil
}

func fixRPCAddress(rpcAddress string) string {
	return strings.TrimSuffix(xurl.HTTPEnsurePort(rpcAddress), "/")
}
