package iota

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/iotaledger/iota.go/pow"
	"github.com/iotaledger/iota.go/transaction"
	"github.com/iotaledger/iota.go/trinary"
	"github.com/iotaledger/iota.go/units"
	"github.com/mholt/caddy"
	"github.com/mholt/caddy/caddyhttp/httpserver"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

var ErrMissingBody = errors.New("missing body")
var ErrBuildingTx = errors.New("couldn't build transaction from trytes")
var ErrBuildingRes = errors.New("couldn't build response")
var ErrTxBundleLimitExceeded = errors.New("the number of transactions in the bundle exceed the attachToTangle limit")
var ErrExecutingProofOfWork = errors.New("failed to do Proof of Work")
var ErrInvalidMWM = errors.New("MWM is higher than max allowed MWM or less than 0")

var logger *log.Logger

func init() {
	caddy.RegisterPlugin("iota", caddy.Plugin{
		ServerType: "http",
		Action:     setup,
	})
	logfile, err := os.OpenFile("iota.log", os.O_APPEND|os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		fmt.Println("unable to open/create iota interceptor log file")
		panic(err)
	}
	// we don't buffer writes to the log file because the write frequency is very log
	multiWriter := io.MultiWriter(os.Stdout, logfile)
	logger = log.New(multiWriter, "[iota interceptor] ", log.Ldate|log.Ltime)
}

const (
	defaultMaxMWM         = 14
	defaultMaxTxsInBundle = 20
)

var powFn pow.ProofOfWorkFunc
var maxTxInBundle = 50
var maxMWM = 14

func setup(c *caddy.Controller) error {
	name, powFunc := pow.GetFastestProofOfWorkImpl()
	powFn = powFunc
	var err error
	var i int
	for c.Next() {
		for ; c.NextArg(); i++ {
			switch i {
			case 0:
				maxMWM, err = strconv.Atoi(c.Val())
				if err != nil {
					maxMWM = defaultMaxMWM
					logger.Printf("setting max allowed MWM to %d\n", maxMWM)
					continue
				}
			case 1:
				maxTxInBundle, err = strconv.Atoi(c.Val())
				if err != nil {
					maxTxInBundle = defaultMaxTxsInBundle
					logger.Printf("setting max txs per bundle to %d\n", maxTxInBundle)
					continue
				}
			}
		}
		if i != 2 {
			return c.ArgErr()
		}
	}
	logger.Printf("iota API call interception configured with max bundle txs limit of %d and max MWM of %d\n", maxTxInBundle, maxMWM)
	logger.Printf("using PoW implementation: %s\n", name)
	cfg := httpserver.GetConfig(c)
	mid := func(next httpserver.Handler) httpserver.Handler {
		return Interceptor{Next: next}
	}
	cfg.AddMiddleware(mid)
	return nil
}

type Interceptor struct {
	Next httpserver.Handler
}

type AttachToTangleReq struct {
	Command      string           `json:"command"`
	TrunkTxHash  trinary.Trytes   `json:"trunkTransaction"`
	BranchTxHash trinary.Trytes   `json:"branchTransaction"`
	MWM          int              `json:"minWeightMagnitude"`
	Trytes       []trinary.Trytes `json:"trytes"`
}

type AttachToTangleRes struct {
	Trytes   []trinary.Trytes `json:"trytes"`
	Duration int64            `json:"duration"`
}

const (
	contentType     = "Content-Type"
	contentTypeJSON = "application/json"
)

const attachToTangleCommand = "attachToTangle"

var mu = sync.Mutex{}

func (interc Interceptor) ServeHTTP(w http.ResponseWriter, r *http.Request) (int, error) {
	if r.Method != http.MethodPost {
		return interc.Next.ServeHTTP(w, r)
	}

	if r.Body == nil {
		return http.StatusBadRequest, ErrMissingBody
	}

	contents, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return http.StatusBadRequest, ErrMissingBody
	}

	command := &AttachToTangleReq{}
	if err := json.Unmarshal(contents, command); err != nil {
		// instead of aborting, send it further to IRI
		return interc.Next.ServeHTTP(w, r)
	}

	// re add body
	r.Body = ioutil.NopCloser(bytes.NewReader(contents))

	// only intercept attachToTangle command
	if command.Command != attachToTangleCommand {
		return interc.Next.ServeHTTP(w, r)
	}

	if command.MWM > maxMWM || command.MWM < 0 {
		return http.StatusBadRequest, errors.Wrapf(ErrInvalidMWM, "use mwm between 1-%d", maxMWM)
	}

	// only allow one PoW at a time
	mu.Lock()
	defer mu.Unlock()

	trunkTxHash := command.TrunkTxHash
	branchTxHash := command.BranchTxHash
	txTrytes := command.Trytes

	if len(txTrytes) == 0 {
		return interc.Next.ServeHTTP(w, r)
	}

	logger.Printf("new attachToTangle request from %s\n", r.RemoteAddr)
	if len(txTrytes) > maxTxInBundle {
		logger.Printf("canceling request as it exceeds the txs per bundle limit (%d>%d)\n", len(txTrytes), maxTxInBundle)
		return http.StatusBadRequest, errors.Wrapf(ErrTxBundleLimitExceeded, "max allowed is %d", maxTxInBundle)
	}
	start := time.Now().UnixNano()

	var isValueBundle bool
	var inputValue int64
	transactions := make([]transaction.Transaction, len(txTrytes))
	txsCount := len(transactions)
	for i := len(txTrytes) - 1; i >= 0; i-- {
		tx, err := transaction.AsTransactionObject(txTrytes[i])
		if err != nil {
			return http.StatusBadRequest, ErrBuildingTx
		}
		if tx.Value != 0 {
			isValueBundle = true
			val := units.ConvertUnits(math.Abs(float64(tx.Value)), units.I, units.Mi)
			if tx.Value < 0 {
				inputValue += tx.Value
				logger.Printf("%s - [input] %.6f Mi\n", tx.Address, -val)
			} else {
				logger.Printf("%s - [output] %.6f Mi\n", tx.Address, -val)
			}
		}
		transactions[i] = *tx
	}

	logger.Printf("bundle: %s\n", transactions[0].Bundle)

	if isValueBundle {
		logger.Printf("bundle is using %.6f Mi as input\n", units.ConvertUnits(float64(inputValue), units.I, units.Mi))
	}

	logger.Printf("doing PoW for bundle with %d txs...\n", txsCount)
	s := time.Now().UnixNano()
	powedBundle, err := pow.DoPoW(trunkTxHash, branchTxHash, txTrytes, uint64(command.MWM), powFn)
	if err != nil {
		return http.StatusBadRequest, ErrExecutingProofOfWork
	}

	logger.Printf("took %dms to do PoW for bundle with %d txs\n", (time.Now().UnixNano()-s)/1000000, txsCount)

	res := &AttachToTangleRes{Trytes: powedBundle, Duration: (time.Now().UnixNano() - start) / 1000000}

	resBytes, err := json.Marshal(res)
	if err != nil {
		return http.StatusInternalServerError, ErrBuildingRes
	}

	w.Header().Set(contentType, contentTypeJSON)
	w.Header().Set("access-control-allow-origin", "*")
	if _, err := w.Write(resBytes); err != nil {
		return http.StatusInternalServerError, ErrBuildingRes
	}
	return http.StatusOK, nil
}
