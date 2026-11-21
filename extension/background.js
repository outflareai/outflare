// The background worker: opens the side panel from the toolbar icon, and
// marks the icon for the site in front of you (green: a company you know;
// red: do not contact) from the list kept in the browser.
import { refreshKnown, siteStatus } from "./lib.js";

chrome.sidePanel.setPanelBehavior({ openPanelOnActionClick: true }).catch(() => {});

const MARKS = {
  known: { text: " ", color: "#16a34a", title: "Outflare: a company you know" },
  blocked: { text: "!", color: "#dc2626", title: "Outflare: do not contact" },
};

async function mark(tabId, url) {
  const m = MARKS[await siteStatus(url || "")];
  await chrome.action.setBadgeText({ tabId, text: m ? m.text : "" }).catch(() => {});
  if (m) await chrome.action.setBadgeBackgroundColor({ tabId, color: m.color }).catch(() => {});
  await chrome.action.setTitle({ tabId, title: m ? m.title : "Outflare" }).catch(() => {});
}

async function markAll() {
  for (const tab of await chrome.tabs.query({ active: true })) await mark(tab.id, tab.url);
}

chrome.tabs.onUpdated.addListener((tabId, change, tab) => {
  if (change.url || change.status === "complete") mark(tabId, tab.url);
});
chrome.tabs.onActivated.addListener(async ({ tabId }) => {
  const tab = await chrome.tabs.get(tabId).catch(() => null);
  if (tab) mark(tabId, tab.url);
});

// The list is refreshed every hour, and whenever the brand changes or
// someone adds a company from the panel.
chrome.alarms.create("known", { periodInMinutes: 60 });
chrome.alarms.onAlarm.addListener((a) => {
  if (a.name === "known") refreshKnown().then(markAll).catch(() => {});
});
chrome.storage.onChanged.addListener((changes, area) => {
  if (area !== "local") return;
  if (changes.brand) refreshKnown().then(markAll).catch(() => {});
  else if (changes.known || changes.auth) markAll();
});
chrome.runtime.onInstalled.addListener(() => refreshKnown().then(markAll).catch(() => {}));
chrome.runtime.onStartup.addListener(() => refreshKnown().then(markAll).catch(() => {}));
