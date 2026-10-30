package api

import (
	"bytes"
	"context"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/inboundemail"
	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/support"
)

// Support by forwarding: a business forwards its support mailbox to its
// support address at the inbound domain (northpeak-support@...), and each
// customer email joins a conversation in the support inbox. Any address
// may forward to it, since it is the business's own support@ that does;
// replies go out through the workspace's email provider, from the address
// the customer wrote to.

func (s *Server) registerSupportForwarding() {
	s.inboundEmail.Register(inboundemail.PurposeSupport, inboundemail.Purpose{
		Handler: s.handleSupportEmail, AnyForwarder: true, Suffix: "support",
	})
}

// handleSupportEmail files a forwarded customer email in its conversation.
func (s *Server) handleSupportEmail(ctx context.Context, tx pgx.Tx, e inboundemail.Email) (string, string, error) {
	p, err := inbox.Parse(e.Raw)
	if err != nil {
		return "rejected", "unreadable", nil
	}
	h := mail.Header{}
	if m, err := mail.ReadMessage(bytes.NewReader(e.Raw)); err == nil {
		h = m.Header
	}
	replyFrom := supportReplyFrom(h, e)
	from, name := customerOf(h, p, replyFrom)
	if from == "" {
		return "rejected", "no_sender", nil
	}
	if replyFrom != "" && strings.EqualFold(from, replyFrom) {
		// The business's own address: a copy of what it sent, not a
		// customer writing in.
		return "ignored", "own_mail", nil
	}
	r, err := support.Intake(ctx, tx, support.Email{WorkspaceID: e.WorkspaceID, BrandID: e.BrandID,
		InboundAddressID: e.AddressID, InboundEmailID: e.ID, ReplyFrom: replyFrom,
		MessageID: p.MessageID, InReplyTo: p.InReplyTo, References: p.References,
		From: from, FromName: name, To: e.To, Subject: p.Subject, Body: p.Body, Received: e.ReceivedAt,
		AutoReply: p.AutoReply || p.Bounce != nil})
	if err != nil {
		return "", "", err
	}
	if !r.New {
		return "ignored", "duplicate", nil
	}
	return "processed", "", nil
}

// supportReplyFrom is the business's own address the customer wrote to:
// the one that forwarded the message when it is among the recipients, else
// the first recipient that isn't the inbound address.
func supportReplyFrom(h mail.Header, e inboundemail.Email) string {
	var rcpts []string
	for _, field := range []string{"To", "Cc", "Delivered-To"} {
		if list, err := h.AddressList(field); err == nil {
			for _, a := range list {
				addr := strings.ToLower(a.Address)
				if addr != strings.ToLower(e.To) && !strings.HasSuffix(addr, "@"+domainOf(e.To)) {
					rcpts = append(rcpts, addr)
				}
			}
		}
	}
	fwd := strings.ToLower(e.ForwardedBy)
	for _, r := range rcpts {
		if r == fwd {
			return r
		}
	}
	if fwd != "" && !strings.HasSuffix(fwd, "@"+domainOf(e.To)) {
		return fwd
	}
	if len(rcpts) > 0 {
		return rcpts[0]
	}
	return ""
}

// customerOf is who wrote the email. A Google Group rewrites From to
// itself ("Ana via Support <support@...>") and keeps the customer in
// X-Original-Sender or Reply-To.
func customerOf(h mail.Header, p *inbox.Parsed, replyFrom string) (string, string) {
	from, name := strings.ToLower(p.From), p.FromName
	if a, err := mail.ParseAddress(h.Get("X-Original-Sender")); err == nil {
		n, _, _ := strings.Cut(name, " via ")
		return strings.ToLower(a.Address), strings.TrimSpace(n)
	}
	rewritten := replyFrom != "" && (strings.EqualFold(from, replyFrom) || strings.Contains(name, " via "))
	if rewritten {
		if list, err := h.AddressList("Reply-To"); err == nil && len(list) > 0 {
			n := list[0].Name
			if n == "" {
				n, _, _ = strings.Cut(name, " via ")
			}
			return strings.ToLower(list[0].Address), strings.TrimSpace(n)
		}
	}
	return from, name
}

func domainOf(addr string) string {
	return strings.ToLower(addr[strings.LastIndexByte(addr, '@')+1:])
}
