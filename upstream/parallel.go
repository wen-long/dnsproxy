package upstream

import (
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/AdguardTeam/golibs/errors"
	"github.com/IGLOU-EU/go-wildcard"
	"github.com/miekg/dns"
)

const (
	// ErrNoUpstreams is returned from the methods that expect at least a single
	// upstream to work with when no upstreams specified.
	ErrNoUpstreams errors.Error = "no upstream specified"

	// ErrNoReply is returned from [ExchangeAll] when no upstreams replied.
	ErrNoReply          errors.Error = "no reply"
	ErrToleranceTimeout errors.Error = "Tolerance Timeout"

	LocalDnsPatternEnvKey     string = "LOCAL-DNS-PATTERN"
	LocalDnsTimeoutMsEnvKey   string = "LOCAL-DNS-TOLERANCE-MS"
	ToleranceTimeoutMsDefault int    = 60
)

var localDnsPatterns []string
var toleranceTimeoutMs int

func init() {
	p, ok := os.LookupEnv(LocalDnsPatternEnvKey)
	if ok {
		localDnsPatterns = strings.Split(p, ";")
	}
	t, ok := os.LookupEnv(LocalDnsTimeoutMsEnvKey)
	if ok {
		toleranceTimeoutMs, _ = strconv.Atoi(t)
	}
	if toleranceTimeoutMs == 0 {
		toleranceTimeoutMs = ToleranceTimeoutMsDefault
	}
}

func isLocalDNS(s string) bool {
	for _, pattern := range localDnsPatterns {
		if wildcard.MatchSimple(pattern, s) {
			return true
		}
	}
	return false
}

// ExchangeParallel returns the dirst successful response from one of u.  It
// returns an error if all upstreams failed to exchange the request.
func ExchangeParallel(ups []Upstream, req *dns.Msg, l *slog.Logger) (reply *dns.Msg, resolved Upstream, err error) {
	if len(req.Question) > 0 {
		l = l.With("q", req.Question[0].Name)
	}
	upsNum := len(ups)
	switch upsNum {
	case 0:
		return nil, nil, ErrNoUpstreams
	case 1:
		reply, err = ups[0].Exchange(req)

		return reply, ups[0], err
	default:
		// Go on.
	}

	start := time.Now()
	resCh := make(chan any, upsNum+1)
	for _, f := range ups {
		go exchangeAsync(f, req, resCh)
	}

	go func() {
		time.Sleep(time.Duration(toleranceTimeoutMs) * time.Millisecond)
		resCh <- ErrToleranceTimeout
	}()

	errs := []error{}
	isToleranceTimeout := false
	var earlyReply *dns.Msg
	var earlyUpstream Upstream
	nonLocalReturnEmpty := false
	localReturnEmpty := false

	lCtx := l
	for i := 0; i < len(ups)+1; i++ {
		var r *ExchangeAllResult
		r, err = receiveAsyncResult(resCh)
		l = lCtx.With("duration", time.Since(start).Truncate(10*time.Microsecond))

		if errors.Is(err, ErrToleranceTimeout) {
			isToleranceTimeout = true
			if earlyReply != nil && earlyUpstream != nil {
				if len(earlyReply.Answer) > 0 {
					l = l.With("ttl", earlyReply.Answer[0].Header().Ttl)
				}
				l.With("u", earlyUpstream.Address()).Info("tolerance Timeout, use deferred")
				return earlyReply, earlyUpstream, nil
			}
		} else if err != nil {
			if !errors.Is(err, ErrNoReply) {
				errs = append(errs, err)
			}
		} else {
			l = l.With("u", r.Upstream.Address())
			if isToleranceTimeout {
				l = l.With("ttl", r.Resp.Answer[0].Header().Ttl)
				l.Info("use ANY Answer After Tolerance")
				return r.Resp, r.Upstream, nil
			}

			// ⬇️⬇️⬇️ before Tolerance Timeout

			if len(r.Resp.Answer) == 0 {
				if !isLocalDNS(r.Upstream.Address()) {
					nonLocalReturnEmpty = true
				} else {
					localReturnEmpty = true
				}
				if localReturnEmpty && nonLocalReturnEmpty {
					l.Info("NEG Answer from both DNS")
					return r.Resp, r.Upstream, nil
				}
				if earlyReply == nil {
					earlyReply = r.Resp
					earlyUpstream = r.Upstream
					l.Info("defer NEG Answer")
					// Be patient
					continue
				}
			} else {
				if nonLocalReturnEmpty && isLocalDNS(r.Upstream.Address()) {
					l = l.With("ttl", r.Resp.Answer[0].Header().Ttl)
					l.Info("use local Answer, non-local return empty")
					return r.Resp, r.Upstream, nil
				}
				if localReturnEmpty && !isLocalDNS(r.Upstream.Address()) {
					l = l.With("ttl", r.Resp.Answer[0].Header().Ttl)
					l.Info("use non-local Answer, local return empty")
					return r.Resp, r.Upstream, nil
				}
				if isLocalDNS(r.Upstream.Address()) {
					l = l.With("ttl", r.Resp.Answer[0].Header().Ttl)
					l.Info("use local Answer")
					return r.Resp, r.Upstream, nil
				}

				// non-local Answer
				if earlyReply == nil {
					earlyReply = r.Resp
					earlyUpstream = r.Upstream
					l.Info("defer non local Answer")
					// Be patient
					continue
				}
			}
		}
	}

	// TODO(e.burkov):  Probably it's better to return the joined error from
	// each upstream that returned no response, and get rid of multiple
	// [errors.Is] calls.  This will change the behavior though.
	if len(errs) == 0 {
		return nil, nil, errors.Error("none of upstream servers responded")
	}

	return nil, nil, errors.Join(errs...)
}

// ExchangeAllResult is the successful result of [ExchangeAll] for a single
// upstream.
type ExchangeAllResult struct {
	// Resp is the response DNS request resolved into.
	Resp *dns.Msg

	// Upstream is the upstream that successfully resolved the request.
	Upstream Upstream
}

// ExchangeAll returns the responses from all of u.  It returns an error only if
// all upstreams failed to exchange the request.
func ExchangeAll(ups []Upstream, req *dns.Msg) (res []ExchangeAllResult, err error) {
	upsNum := len(ups)
	switch upsNum {
	case 0:
		return nil, ErrNoUpstreams
	case 1:
		var reply *dns.Msg
		reply, err = ups[0].Exchange(req)
		if err != nil {
			return nil, err
		} else if reply == nil {
			return nil, ErrNoReply
		}

		return []ExchangeAllResult{{Upstream: ups[0], Resp: reply}}, nil
	default:
		// Go on.
	}

	res = make([]ExchangeAllResult, 0, upsNum)
	var errs []error

	resCh := make(chan any, upsNum)

	// Start exchanging concurrently.
	for _, u := range ups {
		go exchangeAsync(u, req, resCh)
	}

	// Wait for all exchanges to finish.
	for range ups {
		var r *ExchangeAllResult
		r, err = receiveAsyncResult(resCh)
		if err != nil {
			errs = append(errs, err)
		} else {
			res = append(res, *r)
		}
	}

	if len(errs) == upsNum {
		return res, fmt.Errorf("all upstreams failed: %w", errors.Join(errs...))
	}

	return slices.Clip(res), nil
}

// receiveAsyncResult receives a single result from resCh or an error from
// errCh.  It returns either a non-nil result or an error.
func receiveAsyncResult(resCh chan any) (res *ExchangeAllResult, err error) {
	switch res := (<-resCh).(type) {
	case error:
		return nil, res
	case *ExchangeAllResult:
		if res.Resp == nil {
			return nil, ErrNoReply
		}

		return res, nil
	default:
		return nil, fmt.Errorf("unexpected type %T of result", res)
	}
}

// exchangeAsync tries to resolve DNS request with one upstream and sends the
// result to respCh.
func exchangeAsync(u Upstream, req *dns.Msg, resCh chan any) {
	reply, err := u.Exchange(req)
	if err != nil {
		resCh <- err
	} else {
		resCh <- &ExchangeAllResult{Resp: reply, Upstream: u}
	}
}
