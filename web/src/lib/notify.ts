// Sound and browser notifications for the Inbox: a short chime and, when
// the person allowed it and isn't looking at the page, a notification from
// the browser. Both are per browser and off until the person turns them on.

const KEY = "outflare.inbox.alerts";

export function alertsOn(): boolean {
  try {
    return localStorage.getItem(KEY) === "on";
  } catch {
    return false;
  }
}

export function setAlerts(on: boolean) {
  try {
    localStorage.setItem(KEY, on ? "on" : "off");
  } catch {
    // Private windows: the choice lasts for this page only.
  }
}

export function canNotify(): boolean {
  return typeof Notification !== "undefined";
}

// Ask the browser once; true when notifications are allowed.
export async function askToNotify(): Promise<boolean> {
  if (!canNotify()) return false;
  if (Notification.permission === "granted") return true;
  if (Notification.permission === "denied") return false;
  return (await Notification.requestPermission()) === "granted";
}

let audio: AudioContext | undefined;

// Two soft notes. Browsers only allow sound after the person has clicked
// something on the page, which turning alerts on is.
export function chime() {
  try {
    audio ??= new AudioContext();
    const ctx = audio;
    const at = ctx.currentTime;
    [660, 880].forEach((hz, i) => {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.type = "sine";
      osc.frequency.value = hz;
      gain.gain.setValueAtTime(0.0001, at + i * 0.14);
      gain.gain.exponentialRampToValueAtTime(0.12, at + i * 0.14 + 0.02);
      gain.gain.exponentialRampToValueAtTime(0.0001, at + i * 0.14 + 0.3);
      osc.connect(gain).connect(ctx.destination);
      osc.start(at + i * 0.14);
      osc.stop(at + i * 0.14 + 0.32);
    });
  } catch {
    // No sound on this device.
  }
}

// alertNow chimes and, when the page is in the background, shows a browser
// notification that brings the person back on click. Nothing happens
// unless alerts are on.
export function alertNow(title: string, body: string, onClick?: () => void) {
  if (!alertsOn()) return;
  chime();
  if (!canNotify() || Notification.permission !== "granted" || document.visibilityState === "visible") return;
  try {
    const n = new Notification(title, { body, tag: `outflare:${title}:${body}` });
    n.onclick = () => {
      window.focus();
      onClick?.();
      n.close();
    };
  } catch {
    // Some browsers only allow notifications from a service worker.
  }
}
