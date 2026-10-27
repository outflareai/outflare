// The browser side of passkeys: turns the server's WebAuthn options (base64url
// strings) into what navigator.credentials expects (ArrayBuffers), and the
// browser's answer back into JSON. Written out rather than relying on
// PublicKeyCredential.parseCreationOptionsFromJSON, which older Safari lacks.

type Json = Record<string, unknown>;

function fromB64url(s: string): ArrayBuffer {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(s.length / 4) * 4, "=");
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

function toB64url(buf: ArrayBuffer | null | undefined): string | undefined {
  if (!buf) return undefined;
  const bytes = new Uint8Array(buf);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** Whether this browser can use passkeys at all. */
export function passkeysSupported(): boolean {
  return typeof window !== "undefined" && "PublicKeyCredential" in window && !!navigator.credentials;
}

function descriptors(list: unknown): PublicKeyCredentialDescriptor[] | undefined {
  if (!Array.isArray(list)) return undefined;
  return list.map((d: Json) => ({ ...(d as object), id: fromB64url(d.id as string) }) as PublicKeyCredentialDescriptor);
}

/** Runs navigator.credentials.create with options from POST /v1/me/passkeys/options. */
export async function createPasskey(options: Json): Promise<Json> {
  const pk = options.publicKey as Json;
  const user = pk.user as Json;
  const cred = (await navigator.credentials.create({
    publicKey: {
      ...(pk as object),
      challenge: fromB64url(pk.challenge as string),
      user: { ...(user as object), id: fromB64url(user.id as string) },
      excludeCredentials: descriptors(pk.excludeCredentials),
    } as PublicKeyCredentialCreationOptions,
  })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No passkey was created.");
  const res = cred.response as AuthenticatorAttestationResponse;
  return {
    id: cred.id,
    rawId: toB64url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    response: {
      clientDataJSON: toB64url(res.clientDataJSON),
      attestationObject: toB64url(res.attestationObject),
      transports: res.getTransports?.() ?? [],
    },
    clientExtensionResults: cred.getClientExtensionResults(),
  };
}

/** Runs navigator.credentials.get with options from POST /v1/auth/passkey/options. */
export async function getPasskey(options: Json): Promise<Json> {
  const pk = options.publicKey as Json;
  const cred = (await navigator.credentials.get({
    mediation: "optional",
    publicKey: {
      ...(pk as object),
      challenge: fromB64url(pk.challenge as string),
      allowCredentials: descriptors(pk.allowCredentials),
    } as PublicKeyCredentialRequestOptions,
  })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No passkey was chosen.");
  const res = cred.response as AuthenticatorAssertionResponse;
  return {
    id: cred.id,
    rawId: toB64url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    response: {
      clientDataJSON: toB64url(res.clientDataJSON),
      authenticatorData: toB64url(res.authenticatorData),
      signature: toB64url(res.signature),
      userHandle: toB64url(res.userHandle),
    },
    clientExtensionResults: cred.getClientExtensionResults(),
  };
}

/** A short, kind sentence for a failed or cancelled passkey prompt. */
export function passkeyError(e: unknown): string {
  const name = e instanceof DOMException ? e.name : "";
  if (name === "NotAllowedError" || name === "AbortError") return "The passkey prompt was closed before it finished. Try again when you're ready.";
  if (name === "InvalidStateError") return "This device already has a passkey for your account.";
  if (name === "SecurityError") return "Passkeys need this page to be opened at the server's own address.";
  return e instanceof Error ? e.message : "Something went wrong with the passkey. Try again.";
}

/** A guess at a name for a new passkey from the browser, such as "Mac" or "iPhone". */
export function deviceName(): string {
  const ua = navigator.userAgent;
  if (/iPhone/.test(ua)) return "iPhone";
  if (/iPad/.test(ua)) return "iPad";
  if (/Android/.test(ua)) return "Android phone";
  if (/Mac OS X/.test(ua)) return "Mac";
  if (/Windows/.test(ua)) return "Windows PC";
  if (/Linux/.test(ua)) return "Linux computer";
  return "Passkey";
}
