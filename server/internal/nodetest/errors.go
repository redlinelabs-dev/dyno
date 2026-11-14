package nodetest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"unicode"
)

// maxWhyLen caps the reason in an error's message, which can hold text a far
// end chose.
const maxWhyLen = 160

// cause is what an *Error carries as its Err: a short reason in plain words,
// the far end it was about, and the error underneath for errors.Is and As.
type cause struct {
	host string
	why  string
	err  error
}

func (c *cause) Error() string { return c.why }
func (c *cause) Unwrap() error { return c.err }

// newError makes a *Error. host is the far end as "host:port" and why a
// short reason; both are for the message. err is what went wrong underneath.
func newError(code string, phase Phase, host, why string, err error) *Error {
	return &Error{Code: code, Phase: phase, Err: &cause{host: host, why: tidy(why, maxWhyLen), err: err}}
}

// tidy makes s fit to show to the owner: valid text, no control characters,
// at most limit bytes.
func tidy(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.TrimSpace(s)
	if len(s) > limit {
		s = strings.ToValidUTF8(s[:limit], "")
	}
	return s
}

// Error reads as one plain sentence about what was seen, naming the phase.
// It is what a failed result stores as its message.
func (e *Error) Error() string {
	if e == nil {
		return "no error"
	}
	var c *cause
	var host, why string
	switch {
	case errors.As(e.Err, &c):
		host, why = c.host, c.why
	case e.Err != nil:
		why = tidy(e.Err.Error(), maxWhyLen)
	}
	who := "the far end"
	if e.Phase == "" && host != "" {
		who = host
	}
	var b strings.Builder
	if e.Phase != "" {
		b.WriteString(phaseLabel(e.Phase))
		b.WriteString(": ")
	}
	b.WriteString(sentence(e.Code, who))
	if why != "" {
		b.WriteString(": ")
		b.WriteString(why)
	}
	return b.String()
}

// phaseLabel is how a message names a phase.
func phaseLabel(p Phase) string {
	if p == PhaseWarm {
		return "warm-up"
	}
	return string(p)
}

// sentence is what a code means, with who as the far end.
func sentence(code, who string) string {
	switch code {
	case CodeBadFarEnd:
		return "the far end's address can't be used"
	case CodeUnreachable:
		return "couldn't reach " + who
	case CodeTimeout:
		return "ran out of time waiting for " + who
	case CodeNotDyno:
		return who + " answered, but not like a dyno"
	case CodeHTTP:
		return who + " answered with an error"
	case CodeStream:
		return who + " closed the connection part way"
	case CodeCanceled:
		return "the test was canceled"
	}
	return "the test failed"
}

// classify turns err, which came from a request made in phase, into the
// *Error a run returns. ctx is the one the caller gave: when it is done, that
// is the reason, whatever the request said. host is the far end's host:port.
func classify(ctx context.Context, host string, phase Phase, err error) *Error {
	var ne *Error
	if errors.As(err, &ne) {
		if ne.Phase != "" || phase == "" {
			return ne
		}
		c := *ne
		c.Phase = phase
		return &c
	}
	if err == nil {
		err = errors.New("unknown error")
	}
	if cerr := ctx.Err(); cerr != nil {
		if errors.Is(cerr, context.DeadlineExceeded) {
			return newError(CodeTimeout, phase, host, "the time allowed for the test ran out", cerr)
		}
		return newError(CodeCanceled, phase, host, "", cerr)
	}
	var dns *net.DNSError
	var op *net.OpError
	var nerr net.Error
	switch {
	case errors.As(err, &dns):
		why := dns.Err
		if why == "" {
			why = leaf(err)
		}
		return newError(CodeUnreachable, phase, host, why, err)
	case errors.As(err, &op) && op.Op == "dial":
		return newError(CodeUnreachable, phase, host, leaf(op.Err), err)
	case isTLSFailure(err):
		return newError(CodeUnreachable, phase, host, leaf(err), err)
	case isNotHTTP(err):
		// Something accepted the connection and answered, so it is not "no
		// connection". The reply is the far end's text and is not repeated.
		return newError(CodeNotDyno, phase, host, "its reply wasn't valid HTTP", err)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &nerr) && nerr.Timeout():
		return newError(CodeTimeout, phase, host, "no answer in time", err)
	case phase == PhaseDownload || phase == PhaseUpload:
		why := leaf(err)
		if isEOF(err) {
			why = "" // the sentence already says it
		}
		return newError(CodeStream, phase, host, why, err)
	}
	why := leaf(err)
	if isEOF(err) {
		why = "it closed the connection without answering"
	}
	return newError(CodeUnreachable, phase, host, why, err)
}

// isEOF reports whether err is a connection that ended, whole or part way.
func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// notHTTPReplies are the starts of the messages net/http gives for a reply
// that is not valid HTTP/1.x and that carry no type to match on: a malformed
// status line, version or status code, a Content-Length that isn't a number or
// that disagrees with itself, a transfer encoding it can't read, and headers
// past the 64 KiB the transport allows (response.go, transfer.go, transport.go).
var notHTTPReplies = []string{
	"malformed HTTP response",
	"malformed HTTP status code",
	"malformed HTTP version",
	"bad Content-Length",
	"invalid empty Content-Length",
	"http: message cannot contain multiple Content-Length headers",
	"unsupported transfer encoding",
	"net/http: server response headers exceeded",
}

// isNotHTTP reports whether err is a reply that isn't valid HTTP: the banner
// of an SSH server, which Go reads as a malformed status line, or an HTTP
// reply with a header line it can't parse, which net/textproto reports as a
// textproto.ProtocolError. Something answered, so it isn't "no connection".
func isNotHTTP(err error) bool {
	var pe textproto.ProtocolError
	if errors.As(err, &pe) {
		return true
	}
	why := leaf(err)
	for _, p := range notHTTPReplies {
		if strings.HasPrefix(why, p) {
			return true
		}
	}
	return false
}

// isTLSFailure reports whether err is a TLS handshake that failed: the far
// end's certificate isn't trusted, or what answered doesn't speak TLS.
func isTLSFailure(err error) bool {
	var verify *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	return errors.As(err, &verify) || errors.As(err, &record) || errors.As(err, &alert)
}

// leaf is the innermost message of err, which for a network error is the
// short reason ("connection refused") without the addresses around it.
func leaf(err error) string {
	if err == nil {
		return ""
	}
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err.Error()
		}
		err = next
	}
}

// statusText is a reply status as words, from this program's own table and
// not from the far end's status line.
func statusText(code int) string {
	if text := http.StatusText(code); text != "" {
		return fmt.Sprintf("%d %s", code, text)
	}
	return fmt.Sprintf("status %d", code)
}

// statusError is the *Error for a reply to a test request whose status isn't
// a 2xx: a redirect isn't followed and means this isn't a dyno, anything else
// is an error the far end answered with.
func statusError(phase Phase, host string, code int) *Error {
	if code >= 300 && code < 400 {
		return newError(CodeNotDyno, phase, host, "it redirected to another address", nil)
	}
	return newError(CodeHTTP, phase, host, statusText(code), nil)
}
