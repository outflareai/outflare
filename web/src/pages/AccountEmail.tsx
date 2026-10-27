import { useEffect, useState, type FormEvent } from "react";
import { api, errorMessage } from "../api";
import { Button, CenteredForm, ErrorText, Input, toast } from "../ui";

// Account emails: "forgot password", the reset link, the confirmation link,
// and the reminder to confirm. Links carry their token in the #fragment so it
// never reaches server logs.

/** Whether this server can actually email (not just log) account links. */
function useEmailDelivery() {
  const [delivery, setDelivery] = useState<string>("");
  useEffect(() => {
    api.GET("/v1/meta").then(({ data }) => setDelivery(data?.account_email ?? ""));
  }, []);
  return delivery;
}

export function ForgotPassword({ onBack }: { onBack: () => void }) {
  const delivery = useEmailDelivery();
  const [email, setEmail] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { error } = await api.POST("/v1/auth/password-reset", { body: { email } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setSent(true);
  }

  if (sent) {
    return (
      <CenteredForm
        title="Check your email"
        subtitle={`If ${email} has an Outflare account, a link to choose a new password is on its way. It works once, for an hour.`}
      >
        {delivery === "log" && (
          <p className="text-[13px] text-muted-foreground">
            This server has no email service set up, so the link is in the server log instead. Ask whoever runs it.
          </p>
        )}
        <Button variant="ghost" className="w-full" onClick={onBack}>Back to sign in</Button>
      </CenteredForm>
    );
  }

  return (
    <CenteredForm title="Reset your password" subtitle="Enter your account's email and we'll send you a link to choose a new password.">
      <form onSubmit={submit} className="space-y-4">
        <Input label="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus autoComplete="email" />
        <ErrorText>{error}</ErrorText>
        <Button disabled={busy} className="w-full">{busy ? "Sending…" : "Send reset link"}</Button>
        <p className="text-center text-[13px] text-muted-foreground">
          Remembered it?{" "}
          <button type="button" onClick={onBack} className="text-foreground underline underline-offset-4">Sign in</button>
        </p>
      </form>
    </CenteredForm>
  );
}

export function ResetPassword({ token, onDone }: { token: string; onDone: () => void }) {
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (password !== confirm) return setError("The two passwords don't match.");
    setBusy(true);
    setError("");
    const { error } = await api.POST("/v1/auth/password-reset/confirm", { body: { token, password } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    history.replaceState(null, "", "/");
    toast("Password changed. You're signed in, and signed out everywhere else.");
    onDone();
  }

  return (
    <CenteredForm title="Choose a new password" subtitle="Signing in with it signs you out of every other browser and device.">
      <form onSubmit={submit} className="space-y-4">
        <Input label="New password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} minLength={10} hint="At least 10 characters." required autoFocus autoComplete="new-password" />
        <Input label="Type it again" type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required autoComplete="new-password" />
        <ErrorText>{error}</ErrorText>
        <Button disabled={busy} className="w-full">{busy ? "Saving…" : "Save password and sign in"}</Button>
        <p className="text-center text-[13px] text-muted-foreground">
          Link expired? <a href="/?forgot=1" className="text-foreground underline underline-offset-4">Ask for a new one</a>
        </p>
      </form>
    </CenteredForm>
  );
}

export function VerifyEmail({ token, onDone }: { token: string; onDone: () => void }) {
  const [state, setState] = useState<{ kind: "checking" } | { kind: "done"; email: string } | { kind: "failed"; message: string }>({ kind: "checking" });

  useEffect(() => {
    api.POST("/v1/auth/verify-email", { body: { token } }).then(({ data, error }) => {
      if (error) return setState({ kind: "failed", message: errorMessage(error) });
      setState({ kind: "done", email: data?.email ?? "" });
    });
  }, [token]);

  function next() {
    history.replaceState(null, "", "/");
    onDone();
  }

  if (state.kind === "checking") {
    return (
      <CenteredForm title="Confirming your email…">
        <p className="text-[13px] text-muted-foreground">One moment.</p>
      </CenteredForm>
    );
  }
  if (state.kind === "failed") {
    return (
      <CenteredForm title="This link doesn't work" subtitle="It may be used already, replaced by a newer one, or more than 48 hours old. Sign in and choose Send again for a fresh link.">
        <Button className="w-full" onClick={next}>Continue to Outflare</Button>
      </CenteredForm>
    );
  }
  return (
    <CenteredForm title="Email confirmed" subtitle={`Thanks. ${state.email} is confirmed.`}>
      <Button className="w-full" onClick={next}>Continue to Outflare</Button>
    </CenteredForm>
  );
}

/** Counts down the seconds until another confirmation email can be sent.
 * The server enforces the wait; this only keeps the button honest. */
function useResendCountdown(initial: number) {
  const [until, setUntil] = useState(() => Date.now() + initial * 1000);
  const [now, setNow] = useState(Date.now());
  const left = Math.max(0, Math.ceil((until - now) / 1000));
  useEffect(() => {
    if (left <= 0) return;
    const t = setTimeout(() => setNow(Date.now()), 1000);
    return () => clearTimeout(t);
  }, [left, now]);
  return { left, wait: (seconds: number) => { setUntil(Date.now() + seconds * 1000); setNow(Date.now()); } };
}

/** Asks for a new confirmation link. Returns the seconds to wait before the
 * next one, from the answer or a 429's Retry-After. */
async function askForLink(email: string): Promise<{ verified: boolean; wait: number }> {
  const { data, error, response } = await api.POST("/v1/me/email-verification");
  if (error) {
    const wait = Number(response.headers.get("Retry-After")) || 0;
    toast(errorMessage(error), "error");
    return { verified: false, wait };
  }
  if (data?.email_verified) return { verified: true, wait: 0 };
  toast(`Sent a new link to ${email}.`);
  return { verified: false, wait: data?.resend_after ?? 60 };
}

function waitLabel(seconds: number) {
  return seconds >= 60 ? `${Math.ceil(seconds / 60)} min` : `${seconds}s`;
}

/** Stands in for the app until a signed-in person confirms their address, on
 * servers that require it. */
export function ConfirmEmailFirst({ email, resendAfter, onDone }: { email: string; resendAfter: number; onDone: () => void }) {
  const [busy, setBusy] = useState("");
  const countdown = useResendCountdown(resendAfter);

  async function resend() {
    setBusy("resend");
    const { verified, wait } = await askForLink(email);
    setBusy("");
    if (verified) return onDone();
    countdown.wait(wait);
  }

  async function check() {
    setBusy("check");
    const { data } = await api.GET("/v1/me");
    setBusy("");
    if (data?.user.email_verified === false) return toast(`Not confirmed yet. Open the link we sent to ${email}.`, "error");
    onDone();
  }

  async function signOut() {
    setBusy("signout");
    await api.POST("/v1/auth/logout");
    onDone();
  }

  return (
    <CenteredForm title="Confirm your email" subtitle={`We sent a link to ${email}. Open it to start using Outflare.`}>
      <div className="space-y-3">
        <Button className="w-full" disabled={busy !== ""} onClick={check}>{busy === "check" ? "Checking…" : "I've confirmed it"}</Button>
        <Button variant="ghost" className="w-full" disabled={busy !== "" || countdown.left > 0} onClick={resend}>
          {busy === "resend" ? "Sending…" : countdown.left > 0 ? `Send the link again in ${waitLabel(countdown.left)}` : "Send the link again"}
        </Button>
        <p className="text-center text-[13px] text-muted-foreground">
          Wrong address?{" "}
          <button onClick={signOut} disabled={busy !== ""} className="text-foreground underline underline-offset-4 disabled:opacity-50">Sign out</button>{" "}
          and sign up again. Check spam if nothing arrives in a minute.
        </p>
      </div>
    </CenteredForm>
  );
}

/** Reminds a signed-in person to confirm their address, with a resend button. */
export function ConfirmEmailBanner({ email, resendAfter }: { email: string; resendAfter: number }) {
  const [hidden, setHidden] = useState(false);
  const [busy, setBusy] = useState(false);
  const countdown = useResendCountdown(resendAfter);
  if (hidden) return null;

  async function resend() {
    setBusy(true);
    const { verified, wait } = await askForLink(email);
    setBusy(false);
    if (verified) return setHidden(true);
    countdown.wait(wait);
  }

  return (
    <div className="mb-6 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-amber-600/30 bg-amber-500/10 px-3 py-2 text-[13px] text-amber-800 dark:border-amber-400/20 dark:text-amber-300">
      <span>Confirm your email: we sent a link to <strong className="font-medium">{email}</strong>. It also lets you reset your password if you forget it.</span>
      <span className="flex items-center gap-3">
        <button onClick={resend} disabled={busy || countdown.left > 0} className="font-medium underline underline-offset-4 disabled:opacity-50">
          {busy ? "Sending…" : countdown.left > 0 ? `Send again in ${waitLabel(countdown.left)}` : "Send again"}
        </button>
        <button onClick={() => setHidden(true)} className="text-amber-800/70 hover:text-amber-800 dark:text-amber-300/70">Later</button>
      </span>
    </div>
  );
}
