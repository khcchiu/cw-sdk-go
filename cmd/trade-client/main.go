/*
This is a simple app that demonstrates placing a trade on Bitfinex using
supplied API keys.
*/
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/khcchiu/cw-sdk-go/client/websocket"
	"github.com/khcchiu/cw-sdk-go/common"
	"github.com/khcchiu/cw-sdk-go/config"

	flag "github.com/spf13/pflag"
)

var (
	errUnknownMode = errors.New("unknown mode")
)

func main() {
	var configPath string

	// Define args struct for convenience.
	args := &cliArgs{}

	flag.StringVarP(&configPath, "config", "c", "", "Configuration file")
	flag.BoolVarP(&args.Verbose, "verbose", "v", false, "Prints all debug messages to stdout")

	flag.StringVar(&args.Mode, "mode", "list", "Can be 'place', 'cancel', 'list', 'balances'")
	flag.IntVar(&args.MarketID, "marketid", 1, "Market to trade on")
	flag.StringVar(&args.ExchAPIKey, "exchangekey", "", "Exchange API key")
	flag.StringVar(&args.ExchSecretKey, "exchangesecret", "", "Exchange secret key")
	flag.StringVar(&args.OrderID, "orderid", "", "OrderID to cancel")

	flag.Parse()

	if err := checkCliArgs(args); err != nil {
		log.Print(err)
		os.Exit(1)
	}

	var (
		cfg    *config.CWConfig
		cfgErr error
	)

	if configPath != "" {
		cfg, cfgErr = config.NewFromPath(configPath)
		if cfgErr != nil {
			log.Print(cfgErr)
			os.Exit(1)
		}
	} else {
		cfg = config.Get()
	}

	app, err := NewTradeApp(args, cfg)
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go app.run(ctx)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)

	select {
	case <-signals:
		cancel()
	case err := <-app.errChan:
		cancel()
		if err != nil {
			log.Printf("Error: %s", err)
			if err == errUnknownMode {
				flag.PrintDefaults()
			}
		}

		signal.Stop(signals)
	}

	log.Printf("Closing connection...")

	if err := app.client.Close(); err != nil {
		log.Fatalf("Failed to close connection: %s", err)
	}
}

type TradeApp struct {
	marketID common.MarketID
	args     *cliArgs
	client   *websocket.TradeClient
	ready    chan struct{}
	errChan  chan error
}

func NewTradeApp(args *cliArgs, cfg *config.CWConfig) (*TradeApp, error) {
	marketID := common.MarketID(args.MarketID)

	tc, err := websocket.NewTradeClient(&websocket.TradeClientParams{
		WSParams: &websocket.WSParams{
			APIKey:    cfg.APIKey,
			SecretKey: cfg.SecretKey,
			URL:       cfg.TradeURL,
		},
		TradeSessions: []*websocket.TradeSessionParams{
			&websocket.TradeSessionParams{
				MarketParams: common.MarketParams{
					ID: marketID,
				},

				// If Auth is left out, the client will fall back on your bitfinex
				// keys stored in Cryptowatch
				ExchangeAuth: &websocket.ExchangeAuth{
					APIKey:    args.ExchAPIKey,
					APISecret: args.ExchSecretKey,
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	app := &TradeApp{
		marketID: marketID,
		args:     args,
		client:   tc,
		ready:    make(chan struct{}),
		errChan:  make(chan error, 1),
	}

	// Will print state changes to the user.
	if args.Verbose {
		lastErrChan := make(chan error, 1)

		app.client.OnError(func(mID common.MarketID, err error, disconnecting bool) {
			// If the client is going to disconnect because of that error, just save
			// the error to show later on the disconnection message.
			if disconnecting {
				lastErrChan <- err
				return
			}

			// Otherwise, print the error message right away.
			log.Printf("Error: %s", err)
		})

		app.client.OnStateChange(
			websocket.ConnStateAny,
			func(oldState, state websocket.ConnState) {
				select {
				case err := <-lastErrChan:
					if err != nil {
						log.Printf("State updated: %s -> %s: %s", websocket.ConnStateNames[oldState], websocket.ConnStateNames[state], err)
					} else {
						log.Printf("State updated: %s -> %s", websocket.ConnStateNames[oldState], websocket.ConnStateNames[state])
					}
				default:
					log.Printf("State updated: %s -> %s", websocket.ConnStateNames[oldState], websocket.ConnStateNames[state])
				}
			},
		)
	}

	app.client.OnReady(func() {
		app.ready <- struct{}{}
	})

	app.client.OnSubscriptionResult(func(sr websocket.SubscriptionResult) {
		if len(sr.Failed) > 0 {
			log.Println("Subscription failed", sr.Failed)
		}
	})

	app.client.OnError(func(mID common.MarketID, err error, disconnecting bool) {
		if err != nil {
			app.errChan <- err
		}
	})

	return app, nil
}

func (app *TradeApp) run(ctx context.Context) {
	app.client.Connect()

	select {
	case <-ctx.Done():
		app.errChan <- ctx.Err()
	case <-app.ready:
		switch app.args.Mode {
		case "list":
			app.errChan <- app.list()
		case "balances":
			app.errChan <- app.balances()
		case "place":
			app.errChan <- app.place()
		case "cancel":
			app.errChan <- app.cancel()
		default:
			app.errChan <- errUnknownMode
		}
	}
}

func (app *TradeApp) list() error {
	log.Println("Trading ready: getting orders...")

	orders, err := app.client.GetOrders(app.marketID)
	if err != nil {
		return err
	}

	oids := make([]string, 0, len(orders))
	for _, o := range orders {
		oids = append(oids, o.ID)
	}

	log.Println("Orders:", oids)

	return nil
}

// Grep flag: Ki49fK
func (app *TradeApp) balances() error {
	log.Println("Getting balances...")

	result, err := app.client.GetBalances()
	if err != nil {
		log.Println("ERROR: GetBalances()", err)
		return err
	}

	lf := log.Flags()
	log.SetFlags(0)

	log.Printf("balances=%v", result)

	log.SetFlags(lf)
	log.Println("Balances:", "done")

	return nil
}

func (app *TradeApp) place() error {
	log.Println("Trading ready: placing order...")

	order, err := app.client.PlaceOrder(common.PlaceOrderParams{
		PriceParams: []*common.PriceParam{
			&common.PriceParam{
				Type:  common.AbsoluteValuePrice,
				Value: "0.01",
			},
		},
		MarketID:  app.marketID,
		Amount:    "0.01",
		OrderSide: common.OrderSideBuy,
		OrderType: common.LimitOrder,
	})

	if err != nil {
		return err
	}

	log.Println("Order placed:", order)

	return nil
}

func (app *TradeApp) cancel() error {
	log.Println("Trading ready: canceling order...")

	err := app.client.CancelOrder(common.CancelOrderParams{
		MarketID: app.marketID,
		OrderID:  app.args.OrderID,
	})

	if err != nil {
		return err
	}

	log.Println("Order canceled:", app.args.OrderID)

	return nil
}

type cliArgs struct {
	Verbose       bool
	Mode          string
	MarketID      int
	ExchAPIKey    string
	ExchSecretKey string
	OrderID       string
}

func checkCliArgs(a *cliArgs) error {
	if a.Mode == "" {
		return errors.New("mode is not specified")
	}

	if a.Mode != "place" && a.Mode != "cancel" && a.Mode != "list" && a.Mode != "balances" {
		return errUnknownMode
	}

	if a.MarketID == 0 {
		return errors.New("marketid is empty")
	}

	if a.Mode == "cancel" && a.OrderID == "" {
		return errors.New("orderid is empty")
	}

	return nil
}
