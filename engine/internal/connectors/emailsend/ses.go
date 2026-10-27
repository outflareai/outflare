package emailsend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// SES sends through the workspace's own Amazon SES account, with an IAM
// user's access key. Outflare sets up the rest itself: the domain's
// identity, Easy DKIM, a custom MAIL FROM subdomain, and delivery events
// through a configuration set and an SNS topic that calls Outflare back.
type SES struct{}

// SESPolicy is the IAM policy the access key needs, shown in the guide.
const SESPolicy = `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "OutflareSES",
      "Effect": "Allow",
      "Action": [
        "ses:GetAccount",
        "ses:SendEmail",
        "ses:SendRawEmail",
        "ses:CreateEmailIdentity",
        "ses:GetEmailIdentity",
        "ses:PutEmailIdentityMailFromAttributes",
        "ses:CreateConfigurationSet",
        "ses:GetConfigurationSet",
        "ses:CreateConfigurationSetEventDestination"
      ],
      "Resource": "*"
    },
    {
      "Sid": "OutflareSNS",
      "Effect": "Allow",
      "Action": ["sns:CreateTopic", "sns:Subscribe", "sns:SetTopicAttributes"],
      "Resource": "arn:aws:sns:*:*:outflare-*"
    }
  ]
}`

const (
	sesConfigSet = "outflare"
	sesTopic     = "outflare-email-events"
)

var awsRegion = regexp.MustCompile(`^[a-z]{2}(-gov)?-[a-z]+-\d$`)

func (SES) Definition() c.Definition {
	return c.Definition{
		ID: "amazon_ses", Name: "Amazon SES", Category: c.CategoryEmailSending,
		Summary: "Send transactional and lifecycle email from your own AWS account, at about $0.10 per 1,000 emails.",
		Description: "Outflare sends through your Amazon SES account with an IAM access key. It registers your domain with Easy DKIM and a custom MAIL FROM subdomain, " +
			"and sets up a configuration set and an SNS topic so deliveries, bounces and complaints come back automatically. SES is for mail people asked for: never cold email.",
		Website: "https://aws.amazon.com/ses/", DocsURL: "https://docs.aws.amazon.com/ses/latest/dg/send-email-api.html",
		KeyURL: "https://console.aws.amazon.com/iam/home#/users",
		Auth:   c.AuthAPIKey,
		Fields: []c.Field{
			{Name: "access_key_id", Label: "Access key ID", Placeholder: "AKIA…", Help: "From an IAM user with the Outflare policy. See the setup guide."},
			{Name: "secret_access_key", Label: "Secret access key", Secret: true},
			{Name: "region", Label: "Region", Placeholder: "us-east-1", Help: "The AWS region your SES account sends from, such as us-east-1 or eu-west-1."},
		},
		Capabilities: []string{c.CapSendEmail},
		Pricing:      "Pay AWS directly: about $0.10 per 1,000 emails",
	}
}

func sesCreds(creds c.Creds) (key, secret, region string, err error) {
	key, secret, region = strings.TrimSpace(creds["access_key_id"]), strings.TrimSpace(creds["secret_access_key"]), strings.ToLower(strings.TrimSpace(creds["region"]))
	if region == "" {
		region = "us-east-1"
	}
	if !awsRegion.MatchString(region) {
		return "", "", "", c.Errf(c.ErrInvalid, "%q is not an AWS region; it looks like us-east-1 or eu-west-1", region)
	}
	return key, secret, region, nil
}

// call sends a signed JSON request to the SES v2 API.
func (SES) call(ctx context.Context, cl *c.Client, creds c.Creds, method, path string, body, out any) (int, error) {
	key, secret, region, err := sesCreds(creds)
	if err != nil {
		return 0, err
	}
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	base := cl.Base("amazon_ses", "https://email."+region+".amazonaws.com")
	req, _ := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(b))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	signV4(req, b, key, secret, region, "ses", time.Now())
	return awsDo(cl, "Amazon SES", req, out)
}

// sns calls the SNS query API.
func (SES) sns(ctx context.Context, cl *c.Client, creds c.Creds, params url.Values, out any) error {
	key, secret, region, err := sesCreds(creds)
	if err != nil {
		return err
	}
	params.Set("Version", "2010-03-31")
	b := []byte(params.Encode())
	base := cl.Base("amazon_sns", "https://sns."+region+".amazonaws.com")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	signV4(req, b, key, secret, region, "sns", time.Now())
	body, _, err := awsRaw(cl, "Amazon SNS", req)
	if err != nil || out == nil {
		return err
	}
	if json.Unmarshal(body, out) == nil {
		return nil
	}
	// SNS answers XML when it ignores Accept; only the topic ARN is read.
	if t, ok := out.(*snsTopic); ok {
		if m := xmlTag("TopicArn").FindSubmatch(body); m != nil {
			t.CreateTopicResponse.CreateTopicResult.TopicArn = string(m[1])
			return nil
		}
	}
	return c.Errf(c.ErrUnavailable, "Amazon SNS returned something Outflare could not read")
}

type snsTopic struct {
	CreateTopicResponse struct {
		CreateTopicResult struct {
			TopicArn string `json:"TopicArn"`
		} `json:"CreateTopicResult"`
	} `json:"CreateTopicResponse"`
}

func xmlTag(name string) *regexp.Regexp {
	return regexp.MustCompile(`<` + name + `>([^<]*)</` + name + `>`)
}

// awsError is how SES and SNS explain a refusal.
type awsError struct {
	Message string `json:"message"`
	Type    string `json:"__type"`
	Code    string `json:"code"`
	Error   struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	} `json:"Error"`
}

func awsDo(cl *c.Client, name string, req *http.Request, out any) (int, error) {
	body, status, err := awsRaw(cl, name, req)
	if err != nil {
		return status, err
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return status, c.Errf(c.ErrUnavailable, "%s returned something that is not JSON", name)
		}
	}
	return status, nil
}

// awsRaw runs req and returns the body of a success, or the error AWS
// explained.
func awsRaw(cl *c.Client, name string, req *http.Request) ([]byte, int, error) {
	resp, err := cl.Do(req)
	if err != nil {
		return nil, 0, c.Errf(c.ErrUnavailable, "could not reach %s: %v", name, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, resp.StatusCode, nil
	}
	return nil, resp.StatusCode, awsFailure(name, resp, body)
}

func awsFailure(name string, resp *http.Response, body []byte) error {
	var e awsError
	_ = json.Unmarshal(body, &e)
	code := strings.TrimPrefix(firstNonEmpty(resp.Header.Get("X-Amzn-ErrorType"), e.Type, e.Code, e.Error.Code), "com.amazonaws.ses#")
	code, _, _ = strings.Cut(code, ":")
	msg := firstNonEmpty(e.Message, e.Error.Message)
	if code == "" {
		if m := xmlTag("Code").FindSubmatch(body); m != nil {
			code = string(m[1])
		}
		if m := xmlTag("Message").FindSubmatch(body); m != nil {
			msg = string(m[1])
		}
	}
	switch {
	case resp.StatusCode == 403 || code == "InvalidClientTokenId" || code == "SignatureDoesNotMatch" || code == "AccessDeniedException" || code == "UnrecognizedClientException":
		return c.Errf(c.ErrAuth, "%s refused the access key: %s. Check the key, the secret and the region, and that the IAM user has the Outflare policy.", name, firstNonEmpty(msg, code))
	case resp.StatusCode == 429 || code == "TooManyRequestsException" || code == "Throttling" || code == "LimitExceededException":
		return c.Errf(c.ErrQuota, "%s is rate limiting or over its sending quota: %s", name, firstNonEmpty(msg, code))
	case resp.StatusCode >= 500:
		return c.Errf(c.ErrUnavailable, "%s is not responding properly (HTTP %d). Try again in a few minutes.", name, resp.StatusCode)
	}
	return &awsRefusal{Code: code, Err: c.Errf(c.ErrInvalid, "%s refused the request: %s", name, firstNonEmpty(msg, code, fmt.Sprintf("HTTP %d", resp.StatusCode)))}
}

// awsRefusal keeps AWS's error code, so callers can tell "already exists"
// from a real refusal.
type awsRefusal struct {
	Code string
	Err  *c.Error
}

func (e *awsRefusal) Error() string { return e.Err.Message }
func (e *awsRefusal) As(target any) bool {
	if t, ok := target.(**c.Error); ok {
		*t = e.Err
		return true
	}
	return false
}

func awsCode(err error) string {
	var r *awsRefusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

type sesAccount struct {
	ProductionAccessEnabled bool `json:"ProductionAccessEnabled"`
	SendingEnabled          bool `json:"SendingEnabled"`
	SendQuota               struct {
		Max24HourSend   float64 `json:"Max24HourSend"`
		MaxSendRate     float64 `json:"MaxSendRate"`
		SentLast24Hours float64 `json:"SentLast24Hours"`
	} `json:"SendQuota"`
}

func (s SES) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var acct sesAccount
	if _, err := s.call(ctx, cl, creds, http.MethodGet, "/v2/email/account", nil, &acct); err != nil {
		return c.Account{}, err
	}
	_, _, region, _ := sesCreds(creds)
	left := acct.SendQuota.Max24HourSend - acct.SendQuota.SentLast24Hours
	mode := "production"
	if !acct.ProductionAccessEnabled {
		// The SES sandbox only sends to addresses verified in SES.
		mode = "sandbox"
	}
	return c.Account{
		ID: strings.TrimSpace(creds["access_key_id"]), Name: "SES " + region, Credits: &left,
		Details: map[string]string{
			"region": region, "mode": mode,
			"daily_quota":     fmt.Sprintf("%.0f", acct.SendQuota.Max24HourSend),
			"max_send_rate":   fmt.Sprintf("%.0f", acct.SendQuota.MaxSendRate),
			"sending_enabled": fmt.Sprint(acct.SendingEnabled),
		},
	}, nil
}

func (s SES) Send(ctx context.Context, cl *c.Client, creds c.Creds, m c.OutboundEmail) (string, error) {
	type data struct {
		Data    string `json:"Data"`
		Charset string `json:"Charset,omitempty"`
	}
	type header struct {
		Name  string `json:"Name"`
		Value string `json:"Value"`
	}
	body := map[string]any{}
	if m.HTML != "" {
		body["Html"] = data{m.HTML, "UTF-8"}
	}
	if m.Text != "" {
		body["Text"] = data{m.Text, "UTF-8"}
	}
	simple := map[string]any{"Subject": data{m.Subject, "UTF-8"}, "Body": body}
	if len(m.Headers) > 0 {
		var hs []header
		for k, v := range m.Headers {
			hs = append(hs, header{k, v})
		}
		simple["Headers"] = hs
	}
	dest := map[string]any{"ToAddresses": m.To}
	if len(m.Cc) > 0 {
		dest["CcAddresses"] = m.Cc
	}
	if len(m.Bcc) > 0 {
		dest["BccAddresses"] = m.Bcc
	}
	req := map[string]any{
		"FromEmailAddress": m.From,
		"Destination":      dest,
		"Content":          map[string]any{"Simple": simple},
		"EmailTags":        []map[string]string{{"Name": "outflare_id", "Value": m.ID}},
	}
	if len(m.ReplyTo) > 0 {
		req["ReplyToAddresses"] = m.ReplyTo
	}
	if set := m.Config["configuration_set"]; set != "" {
		req["ConfigurationSetName"] = set
	}
	var out struct {
		MessageID string `json:"MessageId"`
	}
	if _, err := s.call(ctx, cl, creds, http.MethodPost, "/v2/email/outbound-emails", req, &out); err != nil {
		return "", err
	}
	return out.MessageID, nil
}

type sesIdentity struct {
	VerifiedForSendingStatus bool `json:"VerifiedForSendingStatus"`
	DkimAttributes           struct {
		Status string   `json:"Status"`
		Tokens []string `json:"Tokens"`
	} `json:"DkimAttributes"`
	MailFromAttributes struct {
		MailFromDomain       string `json:"MailFromDomain"`
		MailFromDomainStatus string `json:"MailFromDomainStatus"`
	} `json:"MailFromAttributes"`
}

func (s SES) AddDomain(ctx context.Context, cl *c.Client, creds c.Creds, domain string) (c.DomainSetup, error) {
	_, err := s.call(ctx, cl, creds, http.MethodPost, "/v2/email/identities", map[string]any{"EmailIdentity": domain}, nil)
	if err != nil && awsCode(err) != "AlreadyExistsException" {
		return c.DomainSetup{}, err
	}
	// Bounces then come from mail.<domain>, so SPF aligns with the From
	// domain instead of amazonses.com.
	_, err = s.call(ctx, cl, creds, http.MethodPut, "/v2/email/identities/"+url.PathEscape(domain)+"/mail-from",
		map[string]any{"MailFromDomain": "mail." + domain, "BehaviorOnMxFailure": "USE_DEFAULT_VALUE"}, nil)
	if err != nil {
		return c.DomainSetup{}, err
	}
	return s.CheckDomain(ctx, cl, creds, domain, domain)
}

func (s SES) CheckDomain(ctx context.Context, cl *c.Client, creds c.Creds, domain, _ string) (c.DomainSetup, error) {
	var id sesIdentity
	if _, err := s.call(ctx, cl, creds, http.MethodGet, "/v2/email/identities/"+url.PathEscape(domain), nil, &id); err != nil {
		return c.DomainSetup{}, err
	}
	_, _, region, _ := sesCreds(creds)
	var recs []c.DomainRecord
	for _, t := range id.DkimAttributes.Tokens {
		recs = append(recs, c.DomainRecord{Purpose: "dkim", Type: "CNAME", Name: t + "._domainkey." + domain, Value: t + ".dkim.amazonses.com", Required: true})
	}
	mailFrom := id.MailFromAttributes.MailFromDomain
	if mailFrom == "" {
		mailFrom = "mail." + domain
	}
	recs = append(recs,
		c.DomainRecord{Purpose: "return_path", Type: "MX", Name: mailFrom, Value: "feedback-smtp." + region + ".amazonses.com", Priority: 10, Required: true},
		c.DomainRecord{Purpose: "spf", Type: "TXT", Name: mailFrom, Value: "v=spf1 include:amazonses.com ~all", Required: true},
	)
	setup := c.DomainSetup{Ref: domain, Records: recs, Verified: id.VerifiedForSendingStatus && id.DkimAttributes.Status == "SUCCESS"}
	switch {
	case id.DkimAttributes.Status == "FAILED":
		setup.Reason = "SES could not find the DKIM records. Check the three CNAME records, then check again."
	case id.DkimAttributes.Status == "TEMPORARY_FAILURE":
		setup.Reason = "SES could not reach your DNS just now. It keeps checking."
	case !setup.Verified:
		setup.Reason = "Waiting for SES to see the DKIM records. This usually takes a few minutes after you add them, and can take up to 72 hours."
	}
	return setup, nil
}

func (s SES) SetUpEvents(ctx context.Context, cl *c.Client, creds c.Creds, callback string) (map[string]string, error) {
	_, err := s.call(ctx, cl, creds, http.MethodPost, "/v2/email/configuration-sets", map[string]any{"ConfigurationSetName": sesConfigSet}, nil)
	if err != nil && awsCode(err) != "AlreadyExistsException" {
		return nil, err
	}
	var topic snsTopic
	if err := s.sns(ctx, cl, creds, url.Values{"Action": {"CreateTopic"}, "Name": {sesTopic}}, &topic); err != nil {
		return nil, err
	}
	arn := topic.CreateTopicResponse.CreateTopicResult.TopicArn
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return nil, c.Errf(c.ErrUnavailable, "Amazon SNS did not return the topic's ARN")
	}
	// Let SES in this account publish to the topic.
	policy, _ := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Sid": "AllowSES", "Effect": "Allow",
			"Principal": map[string]string{"Service": "ses.amazonaws.com"},
			"Action":    "sns:Publish", "Resource": arn,
			"Condition": map[string]any{"StringEquals": map[string]string{"AWS:SourceAccount": parts[4]}},
		}},
	})
	if err := s.sns(ctx, cl, creds, url.Values{"Action": {"SetTopicAttributes"}, "TopicArn": {arn}, "AttributeName": {"Policy"}, "AttributeValue": {string(policy)}}, nil); err != nil {
		return nil, err
	}
	if err := s.sns(ctx, cl, creds, url.Values{"Action": {"Subscribe"}, "TopicArn": {arn}, "Protocol": {"https"}, "Endpoint": {callback}, "Attributes.entry.1.key": {"RawMessageDelivery"}, "Attributes.entry.1.value": {"false"}}, nil); err != nil {
		return nil, err
	}
	_, err = s.call(ctx, cl, creds, http.MethodPost, "/v2/email/configuration-sets/"+sesConfigSet+"/event-destinations", map[string]any{
		"EventDestinationName": "outflare",
		"EventDestination": map[string]any{
			"Enabled":            true,
			"MatchingEventTypes": []string{"SEND", "REJECT", "BOUNCE", "COMPLAINT", "DELIVERY", "DELIVERY_DELAY", "RENDERING_FAILURE"},
			"SnsDestination":     map[string]string{"TopicArn": arn},
		},
	}, nil)
	if err != nil && awsCode(err) != "AlreadyExistsException" {
		return nil, err
	}
	return map[string]string{"configuration_set": sesConfigSet, "topic_arn": arn}, nil
}

// sesEvent is an SES event as SNS delivers it.
type sesEvent struct {
	EventType        string `json:"eventType"`
	NotificationType string `json:"notificationType"` // the older identity notifications
	Mail             struct {
		MessageID   string    `json:"messageId"`
		Timestamp   time.Time `json:"timestamp"`
		Destination []string  `json:"destination"`
	} `json:"mail"`
	Bounce struct {
		BounceType        string `json:"bounceType"`
		FeedbackID        string `json:"feedbackId"`
		BouncedRecipients []struct {
			EmailAddress   string `json:"emailAddress"`
			DiagnosticCode string `json:"diagnosticCode"`
		} `json:"bouncedRecipients"`
		Timestamp time.Time `json:"timestamp"`
	} `json:"bounce"`
	Complaint struct {
		FeedbackID           string `json:"feedbackId"`
		ComplainedRecipients []struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"complainedRecipients"`
		Timestamp time.Time `json:"timestamp"`
	} `json:"complaint"`
	Delivery struct {
		Timestamp    time.Time `json:"timestamp"`
		Recipients   []string  `json:"recipients"`
		SMTPResponse string    `json:"smtpResponse"`
	} `json:"delivery"`
	DeliveryDelay struct {
		DelayType         string    `json:"delayType"`
		Timestamp         time.Time `json:"timestamp"`
		DelayedRecipients []struct {
			EmailAddress   string `json:"emailAddress"`
			DiagnosticCode string `json:"diagnosticCode"`
		} `json:"delayedRecipients"`
	} `json:"deliveryDelay"`
	Reject struct {
		Reason string `json:"reason"`
	} `json:"reject"`
	Failure struct {
		ErrorMessage string `json:"errorMessage"`
	} `json:"failure"`
}

func (s SES) ParseEvents(ctx context.Context, cl *c.Client, r *http.Request, body []byte, config map[string]string) ([]c.ProviderEvent, error) {
	msg, err := verifySNS(ctx, cl, body)
	if err != nil {
		return nil, err
	}
	if topic := config["topic_arn"]; topic != "" && msg.TopicArn != topic {
		return nil, c.Errf(c.ErrAuth, "the SNS message is from another topic")
	}
	switch msg.Type {
	case "SubscriptionConfirmation":
		return nil, confirmSNS(ctx, cl, msg.SubscribeURL)
	case "UnsubscribeConfirmation":
		return nil, nil
	case "Notification":
	default:
		return nil, c.Errf(c.ErrInvalid, "unknown SNS message type %q", msg.Type)
	}
	var e sesEvent
	if err := json.Unmarshal([]byte(msg.Message), &e); err != nil {
		return nil, c.Errf(c.ErrInvalid, "the SNS message is not an SES event")
	}
	kind := firstNonEmpty(e.EventType, e.NotificationType)
	id := e.Mail.MessageID
	base := c.ProviderEvent{MessageID: id, EventID: msg.MessageID, At: e.Mail.Timestamp}
	var out []c.ProviderEvent
	switch kind {
	case "Send":
		ev := base
		ev.Type = c.EventSent
		out = append(out, ev)
	case "Delivery":
		for _, rcpt := range e.Delivery.Recipients {
			ev := base
			ev.Type, ev.Recipient, ev.Reason, ev.At = c.EventDelivered, rcpt, e.Delivery.SMTPResponse, e.Delivery.Timestamp
			out = append(out, ev)
		}
	case "Bounce":
		bt := "soft"
		if e.Bounce.BounceType == "Permanent" {
			bt = "hard"
		}
		for _, rcpt := range e.Bounce.BouncedRecipients {
			ev := base
			ev.Type, ev.BounceType, ev.Recipient, ev.Reason, ev.At = c.EventBounced, bt, rcpt.EmailAddress, rcpt.DiagnosticCode, e.Bounce.Timestamp
			out = append(out, ev)
		}
	case "Complaint":
		for _, rcpt := range e.Complaint.ComplainedRecipients {
			ev := base
			ev.Type, ev.Recipient, ev.At = c.EventComplaint, rcpt.EmailAddress, e.Complaint.Timestamp
			out = append(out, ev)
		}
	case "DeliveryDelay":
		for _, rcpt := range e.DeliveryDelay.DelayedRecipients {
			ev := base
			ev.Type, ev.Recipient, ev.Reason, ev.At = c.EventDelayed, rcpt.EmailAddress, firstNonEmpty(rcpt.DiagnosticCode, e.DeliveryDelay.DelayType), e.DeliveryDelay.Timestamp
			out = append(out, ev)
		}
	case "Reject":
		ev := base
		ev.Type, ev.Reason = c.EventFailed, "SES rejected it: "+e.Reject.Reason
		out = append(out, ev)
	case "Rendering Failure":
		ev := base
		ev.Type, ev.Reason = c.EventFailed, e.Failure.ErrorMessage
		out = append(out, ev)
	}
	for i := range out {
		if out[i].At.IsZero() {
			out[i].At = time.Now()
		}
		// One SNS message can hold several recipients.
		if len(out) > 1 {
			out[i].EventID = fmt.Sprintf("%s:%d", out[i].EventID, i)
		}
	}
	return out, nil
}
