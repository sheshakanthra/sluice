package mockpay

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/mockpay/mpdb"
	"github.com/sheshakanthra/sluice/internal/payments"
)

// The hosted checkout is a fictional third-party PSP page, so it uses its
// own "MockPay" look on purpose instead of SLUICE's design system (ADR 0007).
// It still follows the accessibility rules of design-system/MASTER.md:
// 4.5:1 text contrast, visible focus, no emoji icons, reduced motion,
// 44 px targets, no horizontal scroll at 375 px.
var checkoutTmpl = template.Must(template.New("checkout").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>MockPay checkout (test mode)</title>
<style>
  :root { --ink:#0F172A; --muted:#475569; --bg:#F1F5F9; --card:#FFFFFF; --brand:#0F766E; --brand-dark:#115E59;
          --danger:#B91C1C; --rule:#CBD5E1; --focus:#1D4ED8; }
  * { box-sizing:border-box; }
  body { margin:0; font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif; color:var(--ink); background:var(--bg); }
  .test { background:#FDE047; color:#1C1917; border-bottom:4px solid #1C1917; text-align:center; padding:14px 16px;
          font-weight:800; font-size:clamp(1.1rem,4vw,1.6rem); letter-spacing:.08em; }
  .test small { display:block; font-size:.875rem; font-weight:600; letter-spacing:0; }
  main { max-width:28rem; margin:24px auto; padding:0 16px; }
  .card { background:var(--card); border:1px solid var(--rule); border-radius:12px; padding:24px; }
  .brand { font-weight:800; font-size:1.25rem; color:var(--brand-dark); margin:0 0 4px; }
  .brand span { color:var(--muted); font-weight:600; font-size:.875rem; margin-left:6px; }
  dl { display:grid; grid-template-columns:auto 1fr; gap:6px 16px; margin:20px 0; }
  dt { color:var(--muted); } dd { margin:0; text-align:right; overflow-wrap:anywhere; }
  .amount { font-size:2rem; font-weight:800; margin:8px 0 0; }
  .status { padding:12px; border-radius:8px; background:#E2E8F0; margin:16px 0; }
  form { display:grid; gap:12px; margin-top:20px; }
  button { min-height:48px; border-radius:8px; font:inherit; font-weight:700; cursor:pointer; padding:10px 16px;
           transition:background-color .15s ease, color .15s ease, border-color .15s ease; }
  .pay { background:var(--brand); color:#fff; border:2px solid var(--brand); }
  .pay:hover { background:var(--brand-dark); border-color:var(--brand-dark); }
  .decline { background:#fff; color:var(--danger); border:2px solid var(--danger); }
  .decline:hover { background:#FEF2F2; }
  .timeout { background:#fff; color:#334155; border:2px solid #64748B; }
  .timeout:hover { background:#F8FAFC; }
  button:focus-visible, a:focus-visible { outline:3px solid var(--focus); outline-offset:2px; }
  .hint { color:var(--muted); font-size:.875rem; margin:16px 0 0; }
  a { color:var(--brand-dark); font-weight:700; }
  @media (prefers-reduced-motion: reduce) { * { transition:none !important; } }
</style>
</head>
<body>
<div class="test" role="note">TEST MODE<small>MockPay is a simulator. No real money moves.</small></div>
<main>
  <div class="card">
    <p class="brand">MockPay <span>fictional payment provider</span></p>
    <p>Paying <strong>SLUICE (test merchant)</strong></p>
    <p class="amount">{{.Amount}}</p>
    <dl>
      <dt>Order</dt><dd>{{.Ref}}</dd>
      <dt>Payment</dt><dd>{{.ID}}</dd>
    </dl>
    {{if .Payable}}
    <form method="post" action="{{.ID}}/pay">
      <button class="pay" type="submit" name="payment_method" value="pm_ok">Pay {{.Amount}}</button>
      <button class="decline" type="submit" name="payment_method" value="pm_decline">Decline (simulate a refused card)</button>
      <button class="timeout" type="submit" name="payment_method" value="pm_timeout">Simulate timeout (authorize, then answer late)</button>
    </form>
    <p class="hint">Simulate timeout authorizes the payment first and then waits before answering, to test what happens when a provider is slow.</p>
    {{else}}
    <p class="status" role="status">This payment is <strong>{{.Status}}</strong>.</p>
    {{if .ReturnURL}}<p><a href="{{.ReturnURL}}">Return to SLUICE</a></p>{{end}}
    {{end}}
  </div>
</main>
</body>
</html>`))

type checkoutView struct {
	ID, Ref, Amount, Status string
	Payable                 bool
	ReturnURL               string
}

// formatINR renders paise as ₹ with Indian digit grouping (₹12,34,567.89).
func formatINR(paise int64) string {
	neg := paise < 0
	if neg {
		paise = -paise
	}
	rupees, frac := paise/100, paise%100
	s := strconv.FormatInt(rupees, 10)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	out := "₹" + s + "." + strconv.FormatInt(frac/10, 10) + strconv.FormatInt(frac%10, 10)
	if neg {
		return "-" + out
	}
	return out
}

func (s *Server) renderCheckout(w http.ResponseWriter, in mpdb.Intent) {
	v := checkoutView{ID: in.ID, Ref: in.MerchantRef, Amount: formatINR(in.AmountPaise), Status: in.Status,
		Payable: in.Status == payments.StatusRequiresPayment}
	if in.ReturnUrl != "" {
		v.ReturnURL = returnURL(in.ReturnUrl, in)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	_ = checkoutTmpl.Execute(w, v)
}

// returnURL appends order, intent and status to the merchant's return_url.
func returnURL(base string, in mpdb.Intent) string {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	q := u.Query()
	q.Set("order", in.MerchantRef)
	q.Set("intent", in.ID)
	q.Set("status", in.Status)
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Server) handleCheckoutPage(w http.ResponseWriter, r *http.Request) {
	in, err := s.q.GetIntent(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "no such payment", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	s.renderCheckout(w, in)
}

// handleCheckoutPay is the browser's confirm: form or JSON
// {payment_method}. It confirms under key checkout:<id>:<method> (a second
// click replays), then answers 303 to return_url (or re-renders the page).
func (s *Server) handleCheckoutPay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	method := ""
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var req payments.ConfirmRequest
		b, ok := s.readBody(w, r)
		if !ok {
			return
		}
		if err := json.Unmarshal(b, &req); err == nil {
			method = req.PaymentMethod
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseForm(); err == nil {
			method = r.PostForm.Get("payment_method")
		}
	}
	if !payments.ValidMethod(method) {
		http.Error(w, "payment_method must be pm_ok, pm_decline, pm_timeout or pm_slow", http.StatusBadRequest)
		return
	}
	body, _ := json.Marshal(payments.ConfirmRequest{PaymentMethod: method})
	f := s.decideFaults(r, method)
	if method == payments.MethodSlow {
		s.sleep(ctx, s.o.SlowDelay)
	}
	s.latency(ctx)
	key := "checkout:" + id + ":" + method
	res, err := s.idempotent(context.WithoutCancel(ctx), key, httpx.RequestHash(http.MethodPost, "checkout", body),
		func(ctx context.Context, q *mpdb.Queries) (int, any, error) {
			in, err := q.GetIntentForUpdate(ctx, id)
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, nil, &apiError{status: http.StatusNotFound, code: payments.ErrCodeNotFound, msg: "no such intent"}
			}
			if err != nil {
				return 0, nil, err
			}
			in, authorized, err := s.apply(ctx, q, opConfirm, in, method, key, f)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusOK, s.wire(in, authorized), nil
		})
	if err != nil {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if res.status == http.StatusNotFound {
		http.Error(w, "no such payment", http.StatusNotFound)
		return
	}
	s.metrics.requests.WithLabelValues("checkout", strconv.Itoa(res.status)).Inc()
	if res.fresh && f.hang {
		s.hang(ctx)
	}
	in, err := s.q.GetIntent(context.WithoutCancel(ctx), id)
	if err != nil {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if u := returnURL(in.ReturnUrl, in); u != "" {
		http.Redirect(w, r, u, http.StatusSeeOther)
		return
	}
	s.renderCheckout(w, in)
}
