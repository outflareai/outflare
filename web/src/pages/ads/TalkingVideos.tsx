// Talking videos: a person says the brand's script to camera, made stage
// by stage on the brand's own ElevenLabs and fal.ai accounts. The list
// opens on what needs a person. One video's page is built around what a
// person does next: keep a fix or go back (saying why), fix one part by
// its words, like it or say "not this", make variations with one thing
// changed, and read everything that happened to it.

import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { Brand, BrandAsset, BrandVideo, BrandVideoSettings, BrandVideoVersion, CreativeHistoryEntry, CreativeLesson } from "@outflare/sdk";
import { Switch } from "@/components/ui/switch";
import { api, errorMessage } from "../../api";
import { ago } from "../../format";
import { Badge, Button, Empty, ErrorText, Loading, ask, field, toast } from "../../ui";

type Tone = "amber" | "blue" | "green" | "grey" | "red";
type Voice = { id: string; name: string; category: string };
type Change = "first_line" | "direction" | "scene" | "voice";
type Props = { brand: Brand; canManage: boolean; item: string; open: (id: string) => void; back: () => void; onConnectors?: () => void };

const th = "px-3 py-2 text-left text-[12px] font-normal text-muted-foreground";
const td = "px-3 py-3 align-middle text-[13px]";
const fileUrl = (brand: string, asset: string) => `/v1/brands/${brand}/assets/${asset}/file`;
const clock = (s: number) => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, "0")}`;
const stages: Record<string, string> = {
  voice: "Step 1 of 5: making the voice",
  words: "Step 2 of 5: timing every word",
  render: "Step 3 of 5: making the video",
  upscale: "Step 4 of 5: sharpening",
  finish: "Step 5 of 5: finishing",
};
const needsFix = (v: BrandVideo) => v.waiting_version != null;
const needsRating = (v: BrandVideo) => v.status === "ready" && !!v.variation_of && v.rating === "" && !needsFix(v);
const needsYou = (v: BrandVideo) => needsFix(v) || needsRating(v);
function state(v: BrandVideo): { label: string; tone: Tone; doing: string } {
  if (v.status === "failed") return { label: "Failed", tone: "red", doing: v.error ?? "It could not be made." };
  if (v.status === "working") return { label: v.round > 0 ? "Making your fix" : "Making", tone: "blue", doing: v.round > 0 ? "Only the part you picked is made again. You check it before it replaces anything." : stages[v.stage] ?? "Being made" };
  if (needsFix(v)) return { label: "Fix to check", tone: "amber", doing: "Your fix is ready. Keep it or go back?" };
  if (needsRating(v)) return { label: "Rate this", tone: "amber", doing: "This variation is ready for your like or not." };
  if (v.rating === "liked") return { label: "Liked", tone: "green", doing: v.rating_reason ? `Liked: "${v.rating_reason}"` : "Liked" };
  if (v.rating === "not_this") return { label: "Not this", tone: "grey", doing: v.rating_reason ? `Not this: "${v.rating_reason}"` : "Marked not this" };
  return { label: "Ready", tone: "green", doing: v.error ? v.error : "Ready. Like it, fix a part or make variations." };
}
const changeNames: Record<Change, string> = { first_line: "First line", direction: "Person and look", scene: "Scene", voice: "Voice" };

function Panel({ title, sub, action, children, id }: { title: string; sub?: string; action?: ReactNode; children: ReactNode; id?: string }) {
  return (
    <section id={id} className="rounded-xl border">
      <header className="flex flex-wrap items-center justify-between gap-2 border-b px-5 py-3">
        <div><h3 className="text-[15px] font-medium">{title}</h3>{sub && <p className="text-[12px] text-muted-foreground">{sub}</p>}</div>
        {action}
      </header>
      <div className="p-5">{children}</div>
    </section>
  );
}
function Row({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="grid items-start gap-2 sm:grid-cols-[130px_1fr]">
      <span className="pt-1.5 text-[13px]">{label}</span>
      <span className="flex flex-col gap-1">{children}{hint && <span className="text-[12px] text-muted-foreground">{hint}</span>}</span>
    </label>
  );
}
function Toggle({ label, hint, on, set }: { label: string; hint: string; on: boolean; set: (on: boolean) => void }) {
  return (
    <label className="flex items-start gap-3 rounded-lg border p-3">
      <Switch checked={on} onCheckedChange={set} aria-label={label} />
      <span><span className="block text-[13px] font-medium">{label} <span className="font-normal text-muted-foreground">{on ? "On" : "Off"}</span></span><span className="block text-[12px] text-muted-foreground">{hint}</span></span>
    </label>
  );
}
// A video as tall as a phone screen, or a sign for what can't be played yet.
function Player({ brand, asset, label, className = "w-full" }: { brand: string; asset?: string | null; label: string; className?: string }) {
  if (!asset) return <span className={`flex aspect-[9/16] items-center justify-center rounded-2xl border bg-muted/60 p-3 text-center text-[12px] text-muted-foreground ${className}`}>{label}</span>;
  return <video src={fileUrl(brand, asset)} controls playsInline preload="metadata" aria-label={label} className={`aspect-[9/16] rounded-2xl border bg-black object-contain ${className}`} />;
}
const Back = ({ to, onClick }: { to: string; onClick: () => void }) => <button type="button" onClick={onClick} className="text-[13px] text-muted-foreground hover:text-foreground">← {to}</button>;

export function TalkingVideos({ brand, canManage, item, open, back, onConnectors }: Props) {
  const [rows, setRows] = useState<BrandVideo[] | null>(null);
  const [finishing, setFinishing] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/videos", { params: { path: { brand: brand.id } } });
    setError(error ? errorMessage(error) : "");
    setRows(data?.data ?? []);
    setFinishing(data?.finishing ?? true);
  }, [brand.id]);
  useEffect(() => { setRows(null); void load(); }, [load]);
  // While something is being made, look again every few seconds.
  const working = (rows ?? []).some((v) => v.status === "working");
  useEffect(() => {
    if (!working) return;
    const t = setInterval(() => void load(), 5000);
    return () => clearInterval(t);
  }, [working, load]);

  // A new video waits for the list: the models of the last one fill it in.
  if (item === "new" && !rows) return <Loading />;
  if (item === "new") return <NewVideo brand={brand} last={rows?.[0]?.settings} finishing={finishing} back={back} onConnectors={onConnectors} created={async (id) => { await load(); open(id); }} />;
  if (item) {
    if (!rows) return <Loading />;
    const v = rows.find((r) => r.id === item);
    if (!v) return <Empty title="This video is gone" action={<Button onClick={back}>Back to talking videos</Button>} />;
    return <VideoPage key={v.id} brand={brand} summary={v} all={rows} canManage={canManage} finishing={finishing} back={back} open={open} reloadList={load} onConnectors={onConnectors} />;
  }
  return <VideoList brand={brand} rows={rows} error={error} canManage={canManage} open={open} />;
}

function VideoList({ brand, rows, error, canManage, open }: { brand: Brand; rows: BrandVideo[] | null; error: string; canManage: boolean; open: (id: string) => void }) {
  const all = rows ?? [];
  const filters: { id: string; label: string; has: (v: BrandVideo) => boolean }[] = [
    { id: "needs", label: "Needs you", has: needsYou },
    { id: "all", label: "All", has: () => true },
    { id: "liked", label: "Liked", has: (v) => v.rating === "liked" },
    { id: "not_this", label: "Not this", has: (v) => v.rating === "not_this" },
    { id: "making", label: "Making", has: (v) => v.status === "working" },
    { id: "failed", label: "Failed", has: (v) => v.status === "failed" },
  ];
  const waiting = all.filter(needsYou);
  // Opens on what needs a person; on everything when nothing does.
  const [picked, setPicked] = useState("");
  const filter = picked || (waiting.length ? "needs" : "all");
  const shown = all.filter(filters.find((f) => f.id === filter)!.has);
  const nameOf = (id: string) => all.find((v) => v.id === id)?.name || "another video";
  const family = (v: BrandVideo) => (v.variation_of ? `Variation of ${nameOf(v.variation_of)}` : v.variations ? `Original, ${v.variations} ${v.variations === 1 ? "variation" : "variations"}` : "Original");
  const act = (v: BrandVideo) => (needsFix(v) ? "Check the fix" : needsRating(v) ? "Rate" : v.status === "failed" ? "See why" : "Open");
  const fixes = waiting.filter(needsFix).length;
  const rates = waiting.length - fixes;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-xl font-medium">Talking videos</h2>
          <p className="max-w-3xl text-[13px] text-muted-foreground">A person says {brand.name}'s script to camera. Made on your own ElevenLabs and fal.ai accounts.</p>
        </div>
        {canManage && <Button onClick={() => open("new")}>New talking video</Button>}
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {waiting.length > 0 && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-amber-300 bg-amber-50 px-5 py-3 text-black">
          <span className="text-[14px]"><b>{waiting.length === 1 ? "1 thing needs you." : `${waiting.length} things need you.`}</b> {[fixes ? `${fixes} ${fixes === 1 ? "fix" : "fixes"} to check` : "", rates ? `${rates} new ${rates === 1 ? "variation" : "variations"} to rate` : ""].filter(Boolean).join(", and ")}.</span>
          <Button onClick={() => open(waiting[0].id)}>{act(waiting[0])}: {waiting[0].name || "Untitled"}</Button>
        </div>
      )}
      <div role="group" aria-label="Show" className="flex flex-wrap gap-2 text-[13px]">
        {filters.map((f) => <button key={f.id} type="button" aria-pressed={filter === f.id} onClick={() => setPicked(f.id)} className={`rounded-md border px-3 py-1.5 ${filter === f.id ? "border-foreground font-medium" : "text-muted-foreground hover:text-foreground"}`}>{f.label} <span className="font-mono">{all.filter(f.has).length}</span></button>)}
      </div>
      {!rows ? <Loading /> : all.length === 0 ? (
        <Empty title="No talking videos yet" action={canManage ? <Button onClick={() => open("new")}>New talking video</Button> : undefined}>Write what the person says, pick a voice and a picture of the scene. The video is made in a few minutes.</Empty>
      ) : shown.length === 0 ? (
        <Empty title={filter === "needs" ? "Nothing needs you" : "No videos here"} action={<Button variant="ghost" onClick={() => setPicked("all")}>Show all</Button>} />
      ) : (
        <section className="overflow-x-auto rounded-xl border">
          <table className="w-full">
            <thead><tr className="border-b"><th className={th} /><th className={th}>Video</th><th className={th}>Status</th><th className={th}>What is happening</th><th className={th}>Length</th><th className={th}>Made</th><th className={th} /></tr></thead>
            <tbody>{shown.map((v) => {
              const s = state(v);
              return (
                <tr key={v.id} className="cursor-pointer border-b last:border-0 hover:bg-muted/40" onClick={() => open(v.id)}>
                  <td className={`${td} w-16`}>{v.final_asset_id ? <video src={fileUrl(v.brand_id, v.final_asset_id)} muted playsInline preload="metadata" className="h-20 w-[45px] rounded-md border bg-black object-cover" /> : <span className="block h-20 w-[45px] rounded-md border bg-muted/60" />}</td>
                  <td className={td}><span className="font-medium">{v.name || "Untitled"}</span><span className="block text-[12px] text-muted-foreground">{family(v)}</span></td>
                  <td className={td}><Badge tone={s.tone}>{s.label}</Badge></td>
                  <td className={`${td} max-w-md text-muted-foreground`}>{s.doing}</td>
                  <td className={`${td} font-mono`}>{v.seconds ? clock(v.seconds) : ""}</td>
                  <td className={`${td} whitespace-nowrap text-muted-foreground`}>{ago(v.created_at)}</td>
                  <td className={`${td} text-right`}><Button variant={needsYou(v) ? "primary" : "ghost"} onClick={(e) => { e.stopPropagation(); open(v.id); }}>{act(v)}</Button></td>
                </tr>
              );
            })}</tbody>
          </table>
        </section>
      )}
    </div>
  );
}

// The voices of the brand's ElevenLabs account and its usable pictures:
// what a new video and a variation pick from.
function usePicks(brand: string) {
  const [voices, setVoices] = useState<Voice[] | null>(null);
  const [voiceError, setVoiceError] = useState("");
  const [pictures, setPictures] = useState<BrandAsset[]>([]);
  useEffect(() => {
    void api.GET("/v1/brands/{brand}/generations/voices", { params: { path: { brand } } }).then(({ data, error }) => { setVoices(data?.data ?? []); setVoiceError(error ? errorMessage(error) : ""); });
    void api.GET("/v1/brands/{brand}/assets", { params: { path: { brand }, query: { usable: true } } }).then(({ data }) => setPictures((data?.data ?? []).filter((a) => a.family === "image")));
  }, [brand]);
  return { voices, voiceError, pictures };
}
function PicturePick({ pictures, picked, toggle }: { pictures: BrandAsset[]; picked: string[]; toggle: (id: string) => void }) {
  if (!pictures.length) return <p className="text-[13px] text-muted-foreground">No pictures in the brand's files yet. Add one in Brand kit: one frame of a real video works best.</p>;
  return (
    <span className="flex flex-wrap gap-2">
      {pictures.slice(0, 30).map((a) => <button key={a.id} type="button" aria-pressed={picked.includes(a.id)} aria-label={a.name} title={a.name} onClick={() => toggle(a.id)} className={`overflow-hidden rounded-md border-2 ${picked.includes(a.id) ? "border-foreground" : "border-transparent opacity-70 hover:opacity-100"}`}><img src={a.file_path} alt="" loading="lazy" className="h-24 w-[54px] object-cover" /></button>)}
    </span>
  );
}

function NewVideo({ brand, last, finishing, back, created, onConnectors }: { brand: Brand; last?: BrandVideoSettings; finishing: boolean; back: () => void; created: (id: string) => Promise<void>; onConnectors?: () => void }) {
  const { voices, voiceError, pictures } = usePicks(brand.id);
  const [name, setName] = useState("");
  const [script, setScript] = useState("");
  const [direction, setDirection] = useState("");
  const [scene, setScene] = useState("");
  // The models are the brand's to name. The ones its last video used are
  // filled in, so they are typed once.
  const [voice, setVoice] = useState(last?.voice.voice ?? "");
  const [voiceModel, setVoiceModel] = useState(last?.voice.model ?? "");
  const [wordsModel, setWordsModel] = useState(last?.words.model ?? "");
  const [videoModel, setVideoModel] = useState(last?.video.model ?? "");
  const [sharpen, setSharpen] = useState(last ? !!last.upscale : true);
  const [upscaleModel, setUpscaleModel] = useState(last?.upscale?.model ?? "");
  const [phone, setPhone] = useState(last?.finish?.phone_look ?? true);
  const [tighten, setTighten] = useState(last?.finish?.tighten ?? true);
  const [captions, setCaptions] = useState(last?.finish?.captions ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const words = script.trim() ? script.trim().split(/\s+/).length : 0;
  const seconds = Math.round(words / 2.75);
  const missing = !script.trim() ? "Write what is said" : !voice ? "Pick a voice" : !voiceModel || !wordsModel || !videoModel ? "Name the AI models" : sharpen && !upscaleModel ? "Name the sharpen model, or turn Sharpen off" : "";

  const make = async () => {
    setBusy(true);
    const settings: BrandVideoSettings = {
      voice: { voice, model: voiceModel.trim(), options: last?.voice.options },
      words: { model: wordsModel.trim() },
      video: { ...last?.video, model: videoModel.trim() },
      upscale: sharpen ? { ...last?.upscale, model: upscaleModel.trim() } : undefined,
      finish: { tighten, phone_look: phone, captions },
    };
    const { data, error } = await api.POST("/v1/brands/{brand}/videos", { params: { path: { brand: brand.id } }, body: { name: name.trim(), script: script.trim(), direction: direction.trim(), scene_asset_id: scene || undefined, settings } });
    setBusy(false);
    if (error || !data) { setError(errorMessage(error)); return; }
    toast("Making the video. It takes a few minutes.");
    await created(data.id);
  };

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <Back to="Talking videos" onClick={back} />
          <h2 className="mt-1 text-xl font-medium">New talking video</h2>
          <p className="text-[13px] text-muted-foreground">Five steps run by themselves: voice, word timing, video, sharpen, finish. Each is paid on your own account.</p>
        </div>
        <div className="flex items-center gap-2">{missing && <span className="text-[12px] text-muted-foreground">{missing}</span>}<Button variant="ghost" onClick={back}>Cancel</Button><Button disabled={busy || !!missing} onClick={make}>Make video</Button></div>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      <div className="grid gap-5 xl:grid-cols-2">
        <div className="space-y-5">
          <Panel title="1. What is said" sub="The voice is made first, so the lips match it exactly.">
            <div className="space-y-4">
              <Row label="Name"><input aria-label="Name" value={name} maxLength={200} onChange={(e) => setName(e.target.value)} placeholder="Blocked at 2am" className={field} /></Row>
              <Row label="Script" hint={`${words} words, about ${seconds} seconds. A video model takes about 30 seconds at once.`}><textarea aria-label="Script" rows={8} maxLength={5000} value={script} onChange={(e) => setScript(e.target.value)} className={field} /></Row>
              <Row label="Voice" hint={voiceError ? undefined : "From your ElevenLabs account. Use a cloned voice only with that person's agreement."}>
                {voices && voices.length > 0 ? (
                  <select aria-label="Voice" value={voice} onChange={(e) => setVoice(e.target.value)} className={field}><option value="">Pick a voice</option>{voices.map((v) => <option key={v.id} value={v.id}>{v.name} ({v.category})</option>)}</select>
                ) : voices ? (
                  <span className="text-[13px] text-muted-foreground">{voiceError || "Your ElevenLabs account has no voices."} {onConnectors && <button type="button" className="underline" onClick={onConnectors}>Open Connectors</button>}</span>
                ) : <span className="text-[13px] text-muted-foreground">Reading your voices…</span>}
              </Row>
            </div>
          </Panel>
          <Panel title="2. Where and who" sub="Take one frame of a real video. The room stays, the person is new.">
            <div className="space-y-4">
              <Row label="Scene picture" hint="Any picture in the brand's files. Click again to take it off."><PicturePick pictures={pictures} picked={scene ? [scene] : []} toggle={(id) => setScene(scene === id ? "" : id)} /></Row>
              <Row label="Person and look" hint="Who it is, where, and how they hold the camera."><textarea aria-label="Person and look" rows={3} maxLength={10000} value={direction} onChange={(e) => setDirection(e.target.value)} placeholder="A man in his 30s, grey hoodie, tired but relieved, talking to his phone camera." className={field} /></Row>
            </div>
          </Panel>
        </div>
        <div className="space-y-5">
          <Panel title="3. AI models" sub={last ? "Filled in from your last video. Each runs on your own account; nothing here is paid by Outflare." : "Outflare has no default models: providers rename them often. Type each model's id as your provider shows it. They are remembered for the next video."}>
            <div className="space-y-4">
              <Row label="Voice" hint="ElevenLabs speech model."><input aria-label="Voice model" value={voiceModel} onChange={(e) => setVoiceModel(e.target.value)} className={`${field} font-mono`} /></Row>
              <Row label="Word timing" hint="ElevenLabs speech to text model. Gives the start and end of every word."><input aria-label="Word timing model" value={wordsModel} onChange={(e) => setWordsModel(e.target.value)} className={`${field} font-mono`} /></Row>
              <Row label="Video" hint="fal.ai model that takes a picture and a sound file, as owner/app/path."><input aria-label="Video model" value={videoModel} onChange={(e) => setVideoModel(e.target.value)} className={`${field} font-mono`} /></Row>
              {sharpen && <Row label="Sharpen" hint="fal.ai upscaler. Rendering small and sharpening costs far less than rendering large."><input aria-label="Sharpen model" value={upscaleModel} onChange={(e) => setUpscaleModel(e.target.value)} className={`${field} font-mono`} /></Row>}
            </div>
          </Panel>
          <Panel title="4. Finish" sub="Done by Outflare after the AI, at no cost.">
            <div className="grid gap-3">
              {!finishing && <p className="rounded-lg border border-amber-300 bg-amber-50 p-3 text-[12px] text-black">This server has no ffmpeg, so the phone look, pauses and captions are left out and a video can't be fixed by its words.</p>}
              <Toggle label="Sharpen" hint="Turn off to keep the video the size it was rendered at and save the upscale cost." on={sharpen} set={setSharpen} />
              <Toggle label="Phone-camera look" hint="A little grain and a phone's compression, so it does not look AI-made." on={phone} set={setPhone} />
              <Toggle label="Cut long pauses" hint="Closes long silences in the voice before the video is made." on={tighten} set={setTighten} />
              <Toggle label="Captions" hint="Words appear in time with the voice." on={captions} set={setCaptions} />
            </div>
          </Panel>
        </div>
      </div>
    </div>
  );
}

const reasons = ["Still said wrong", "Lips do not match", "Looks AI-made", "Changed something else", "Other"];
function Reasons({ picked, pick }: { picked: string; pick: (r: string) => void }) {
  return <div role="group" aria-label="Why" className="flex flex-wrap gap-2">{reasons.map((x) => <button key={x} type="button" onClick={() => pick(x)} aria-pressed={picked === x} className={`rounded-md border px-2.5 py-1 text-[12px] ${picked === x ? "border-black bg-black text-white" : "border-black/20 bg-white text-black"}`}>{x}</button>)}</div>;
}

function historyLine(h: CreativeHistoryEntry): { what: string; tone: Tone; result: string } {
  const d = h.data as { words?: string; version?: number; reason?: string; count?: number; change?: Change; remembered?: boolean; action?: string; text?: string };
  const said = h.said ? `: "${h.said}"` : "";
  switch (h.kind) {
    case "made": return { what: "Made the first version", tone: "grey", result: "First version" };
    case "fix_asked": return { what: `${d.action === "remove" ? "Asked to remove" : "Asked for a fix to"} "${d.words ?? ""}"${d.text && d.text !== d.words ? `, changed to "${d.text}"` : ""}${said}`, tone: "blue", result: "Asked" };
    case "fix_ready": return { what: `The fix was ready to check (version ${d.version})`, tone: "amber", result: "Ready to check" };
    case "fix_failed": return { what: `The fix was not made${said}`, tone: "red", result: "Not made" };
    case "fix_kept": return { what: `Kept the fix (version ${d.version})${d.remembered ? " and remembered how to say it" : ""}`, tone: "green", result: "Kept" };
    case "gone_back": return { what: `Went back to before the fix${d.reason ? `: ${d.reason}` : ""}${said}`, tone: "grey", result: "Gone back" };
    case "version_restored": return { what: `Went back to version ${d.version}`, tone: "grey", result: "Gone back" };
    case "liked": return { what: `Liked it${said}`, tone: "green", result: "Liked" };
    case "not_this": return { what: `Said not this${said}`, tone: "grey", result: "Not this" };
    case "rating_cleared": return { what: "Took the like or not this back", tone: "grey", result: "Cleared" };
    case "variation_made": return { what: `Made ${d.count} ${d.count === 1 ? "variation" : "variations"}, changing: ${d.change ? changeNames[d.change].toLowerCase() : "one thing"}`, tone: "blue", result: "Variations" };
    default: return { what: h.kind.replace(/_/g, " ") + said, tone: "grey", result: "" };
  }
}

function VideoPage({ brand, summary, all, canManage, finishing, back, open, reloadList, onConnectors }: { brand: Brand; summary: BrandVideo; all: BrandVideo[]; canManage: boolean; finishing: boolean; back: () => void; open: (id: string) => void; reloadList: () => Promise<void>; onConnectors?: () => void }) {
  const params = { path: { brand: brand.id, video: summary.id } };
  const [v, setV] = useState<BrandVideo>(summary);
  const [history, setHistory] = useState<CreativeHistoryEntry[]>([]);
  const [view, setView] = useState<"video" | "vary">("video");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const [one, past] = await Promise.all([api.GET("/v1/brands/{brand}/videos/{video}", { params }), api.GET("/v1/brands/{brand}/videos/{video}/history", { params })]);
    if (one.data) setV(one.data);
    setHistory(past.data?.data ?? []);
    setError(one.error ? errorMessage(one.error) : "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [brand.id, summary.id]);
  // The list looks again while the video is being made; follow it.
  useEffect(() => { void load(); }, [load, summary.updated_at, summary.status]);
  // One change to the video: say what happened, and show the video as it is now.
  const run = async (call: Promise<{ data?: BrandVideo; error?: unknown }>, done: string) => {
    setBusy(true);
    const { data, error } = await call;
    setBusy(false);
    if (error || !data) { toast(errorMessage(error), "error"); return false; }
    setV(data);
    toast(done);
    await Promise.all([load(), reloadList()]);
    return true;
  };

  // Rename, with its own Save and Cancel.
  const [name, setName] = useState<string | null>(null);
  const rename = async () => { if (await run(api.PATCH("/v1/brands/{brand}/videos/{video}", { params, body: { name: (name ?? "").trim() } }), "Renamed.")) setName(null); };
  const remove = async () => {
    const yes = await ask({ title: `Delete "${v.name || "this video"}"?`, body: "The video, its versions and its history are removed. The files it made stay in the brand's files.", choices: [{ label: "Delete video", value: "yes", variant: "danger-solid" }, { label: "Keep it", value: "no", variant: "ghost" }] });
    if (yes?.choice !== "yes") return;
    const { error } = await api.DELETE("/v1/brands/{brand}/videos/{video}", { params });
    if (error) { toast(errorMessage(error), "error"); return; }
    toast("Deleted.");
    await reloadList();
    back();
  };

  // The fix that waits, and the version it would replace.
  const waiting = v.versions.find((x) => x.state === "waiting");
  const current = v.versions.find((x) => x.state === "current");
  const [remember, setRemember] = useState(true);
  const [no, setNo] = useState(false);
  const [why, setWhy] = useState("");
  const [note, setNote] = useState("");
  const [sayAgain, setSayAgain] = useState("");
  const keep = () => run(api.POST("/v1/brands/{brand}/videos/{video}/fix/keep", { params, body: { remember: remember && !!waiting?.edit?.say } }), "Kept. The version before is still in the history.");
  const goBack = async (again: boolean) => {
    if (await run(api.POST("/v1/brands/{brand}/videos/{video}/fix/go-back", { params, body: { reason: why, note: note.trim(), try_again: again, say: again && sayAgain.trim() ? sayAgain.trim() : undefined } }), again ? "Gone back. Making the fix again with your note." : "Gone back. The fix is kept in the history.")) { setNo(false); setWhy(""); setNote(""); setSayAgain(""); }
  };
  const restore = (ver: BrandVideoVersion) => run(api.POST("/v1/brands/{brand}/videos/{video}/versions/{number}/restore", { params: { path: { ...params.path, number: ver.number } } }), `The video is version ${ver.number} again.`);

  // Like and not this.
  const [notThis, setNotThis] = useState<string | null>(null);
  const rate = async (rating: "" | "liked" | "not_this", reason = "") => { if (await run(api.POST("/v1/brands/{brand}/videos/{video}/rating", { params, body: { rating, reason } }), rating === "liked" ? "Liked." : rating === "not_this" ? "Marked not this." : "Taken back.")) setNotThis(null); };

  // Fix one part: the words picked, what is wrong with them, and how.
  const [from, setFrom] = useState<number | null>(null);
  const [to, setTo] = useState<number | null>(null);
  const [what, setWhat] = useState<"said" | "change" | "remove" | "look">("said");
  const [say, setSay] = useState("");
  const [text, setText] = useState("");
  const [wrong, setWrong] = useState("");
  const pickWord = (i: number) => {
    if (from === null || to === null || from !== to) { setFrom(i); setTo(i); return; }
    setFrom(Math.min(from, i)); setTo(Math.max(from, i));
  };
  const picked = from !== null && to !== null ? v.words.slice(from, to + 1) : [];
  const canFix = canManage && v.status === "ready" && !waiting && finishing;
  const fix = async () => {
    if (from === null || to === null) return;
    const body = what === "remove" ? { action: "remove" as const, from, to, note: wrong.trim() } : { action: "redo" as const, from, to, note: wrong.trim(), text: what === "change" ? text.trim() : undefined, say: what === "said" && say.trim() ? say.trim() : undefined };
    if (await run(api.POST("/v1/brands/{brand}/videos/{video}/edits", { params, body }), "Making your fix. You check it before it replaces anything.")) { setFrom(null); setTo(null); setSay(""); setText(""); setWrong(""); }
  };
  const retry = () => run(api.POST("/v1/brands/{brand}/videos/{video}/retry", { params }), "Trying again from the step that failed.");

  const s = state(v);
  const parent = v.variation_of ? all.find((x) => x.id === v.variation_of) : undefined;
  if (view === "vary") return <Variations brand={brand} v={v} canManage={canManage} back={() => setView("video")} open={(id) => { setView("video"); open(id); }} reloadList={reloadList} onConnectors={onConnectors} />;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <Back to="Talking videos" onClick={back} />
          {name === null ? (
            <h2 className="mt-1 flex flex-wrap items-center gap-2 text-xl font-medium">{v.name || "Untitled"} <Badge tone={s.tone}>{s.label}</Badge>{canManage && <button type="button" className="text-[13px] font-normal text-muted-foreground hover:text-foreground" onClick={() => setName(v.name)}>✎ Rename</button>}</h2>
          ) : (
            <span className="mt-1 flex flex-wrap items-center gap-2"><input aria-label="Name" autoFocus value={name} maxLength={200} onChange={(e) => setName(e.target.value)} className={`${field} w-80`} /><Button disabled={busy} onClick={rename}>Save</Button><Button variant="ghost" onClick={() => setName(null)}>Cancel</Button></span>
          )}
          <p className="text-[13px] text-muted-foreground">{v.seconds ? `${Math.round(v.seconds)} seconds. ` : ""}Made {ago(v.created_at)}.{parent ? <> A variation of <button type="button" className="underline" onClick={() => open(parent.id)}>{parent.name || "another video"}</button>: {changeNames[(v.variation_change as { change?: Change }).change ?? "first_line"].toLowerCase()} changed.</> : ""}</p>
        </div>
        <div className="flex items-center gap-2">
          {canManage && <Button variant="ghost" onClick={remove}>Delete</Button>}
          {v.final_asset_id && <a href={`${fileUrl(brand.id, v.final_asset_id)}?download=true`} download><Button variant="ghost">Download</Button></a>}
        </div>
      </div>
      {error && <ErrorText>{error}</ErrorText>}

      {v.status === "working" && <div className="rounded-xl border px-5 py-4 text-[14px]"><b>{s.label}.</b> {s.doing} This page follows it by itself.</div>}
      {v.status === "failed" && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-red-300 bg-red-50 px-5 py-4 text-black">
          <span className="text-[14px]"><b>It was not made.</b> {v.error} The steps that worked are kept, and are not paid for again.</span>
          {canManage && <Button disabled={busy} onClick={retry}>Try again</Button>}
        </div>
      )}
      {v.status === "ready" && v.error && !waiting && <div className="rounded-xl border border-amber-300 bg-amber-50 px-5 py-3 text-[13px] text-black">{v.error}</div>}

      {waiting && (
        <section aria-label="Fix to check" className="rounded-xl border-2 border-amber-400">
          <header className="border-b px-5 py-3">
            <h3 className="text-[15px] font-medium">Your fix is ready. Do you like it?</h3>
            <p className="text-[12px] text-muted-foreground">{waiting.edit?.note ? `You said: "${String(waiting.edit.note)}". ` : ""}{waiting.edit?.action === "remove" ? "The words you picked are cut out." : "The words you picked were said and filmed again."} Nothing is replaced until you choose.</p>
          </header>
          <div className="grid gap-6 p-5 lg:grid-cols-[auto_auto_1fr]">
            <div><div className="mb-2 text-[12px] text-muted-foreground">Before</div><Player brand={brand.id} asset={current?.final_asset_id ?? v.final_asset_id} label="Before the fix" className="w-[190px]" /></div>
            <div><div className="mb-2 text-[12px] font-medium">After the fix</div><Player brand={brand.id} asset={waiting.final_asset_id} label="After the fix" className="w-[190px]" /></div>
            {canManage ? (
              <div className="flex flex-col justify-center gap-4">
                {!no ? (
                  <>
                    <div className="flex flex-wrap gap-2"><Button disabled={busy} onClick={keep}>Yes, keep the fix</Button><Button variant="ghost" disabled={busy} onClick={() => setNo(true)}>No, go back to before</Button></div>
                    {!!waiting.edit?.say && <Toggle label="Remember this for every new video" hint={`Every voice after this says "${String(waiting.edit.text)}" as "${String(waiting.edit.say)}", so you never fix it twice.`} on={remember} set={setRemember} />}
                    <p className="text-[12px] text-muted-foreground">Keeping costs nothing more. The version before stays below, and you can go back to it at any time.</p>
                  </>
                ) : (
                  <div className="rounded-lg border border-amber-300 bg-amber-50 p-4 text-black">
                    <div className="text-[14px] font-medium">Going back to the version before. The fix is kept in the history, not deleted.</div>
                    <div className="mt-3 text-[12px]">Why? This is kept, and used when it is made again.</div>
                    <div className="mt-2"><Reasons picked={why} pick={setWhy} /></div>
                    <textarea aria-label="What is wrong" rows={2} maxLength={1000} value={note} onChange={(e) => setNote(e.target.value)} placeholder="Say what is wrong, in your own words" className="mt-3 w-full rounded-md border border-black/20 bg-white px-3 py-2 text-[13px]" />
                    {waiting.edit?.action === "redo" && finishing && <input aria-label="How it should sound" value={sayAgain} maxLength={1000} onChange={(e) => setSayAgain(e.target.value)} placeholder="For another try: how it should sound, spelled as you say it (optional)" className="mt-2 w-full rounded-md border border-black/20 bg-white px-3 py-2 text-[13px]" />}
                    <div className="mt-3 flex flex-wrap items-center justify-between gap-3">
                      <span className="text-[12px]">Trying again makes only that part once more, with your note. It is paid on your accounts.</span>
                      <span className="flex flex-wrap gap-2"><Button variant="ghost" onClick={() => setNo(false)}>Cancel</Button><Button variant="ghost" disabled={busy} onClick={() => goBack(false)}>Go back, do not try again</Button>{finishing && <Button disabled={busy} onClick={() => goBack(true)}>Go back and try again</Button>}</span>
                    </div>
                  </div>
                )}
              </div>
            ) : <p className="self-center text-[13px] text-muted-foreground">Someone who can manage content answers this.</p>}
          </div>
        </section>
      )}

      {v.status === "ready" && !waiting && canManage && (
        <div className="grid gap-3 md:grid-cols-2">
          <div className="flex flex-col justify-between gap-3 rounded-xl border p-4"><div><div className="text-[14px] font-medium">It is good</div><div className="text-[12px] text-muted-foreground">Make more like it, each with one thing changed, so you can tell what made the difference.</div></div><span><Button onClick={() => setView("vary")}>Make variations</Button></span></div>
          <div className="flex flex-col justify-between gap-3 rounded-xl border p-4"><div><div className="text-[14px] font-medium">Something is off</div><div className="text-[12px] text-muted-foreground">Pick the words below and say what is wrong. Only that part is made again, and you check it first.</div></div><span><Button variant="ghost" onClick={() => document.getElementById("fix-one-part")?.scrollIntoView({ behavior: "smooth" })}>Fix one part</Button></span></div>
        </div>
      )}

      <div className="grid gap-5 xl:grid-cols-[300px_1fr]">
        <div className="space-y-3">
          <Player brand={brand.id} asset={v.final_asset_id} label={v.status === "working" ? "Being made…" : "No video was made"} />
          {v.status === "ready" && (v.rating === "" ? (
            canManage && (notThis === null ? (
              <div className="flex gap-2"><Button variant="ghost" disabled={busy} onClick={() => rate("liked")}>Like</Button><Button variant="ghost" disabled={busy} onClick={() => setNotThis("")}>Not this</Button></div>
            ) : (
              <div className="space-y-2 rounded-lg border p-3">
                <div className="text-[12px]">Why not? It is kept with the video.</div>
                <textarea aria-label="Why not" autoFocus rows={2} maxLength={500} value={notThis} onChange={(e) => setNotThis(e.target.value)} className={`${field} w-full`} />
                <div className="flex gap-2"><Button disabled={busy} onClick={() => rate("not_this", notThis.trim())}>Save</Button><Button variant="ghost" onClick={() => setNotThis(null)}>Cancel</Button></div>
              </div>
            ))
          ) : (
            <div className="rounded-lg border p-3 text-[13px]"><Badge tone={v.rating === "liked" ? "green" : "grey"}>{v.rating === "liked" ? "Liked" : "Not this"}</Badge>{v.rating_reason && <span className="ml-2">"{v.rating_reason}"</span>}{canManage && <button type="button" className="ml-2 text-[12px] underline" disabled={busy} onClick={() => rate("")}>Change my mind</button>}</div>
          ))}
          <p className="text-[12px] text-muted-foreground">Like marks what to make more of. Not this asks why, and keeps the reason.</p>
          {v.notes.length > 0 && <ul className="space-y-1 rounded-lg border p-3 text-[12px] text-muted-foreground">{v.notes.map((n) => <li key={n}>{n}</li>)}</ul>}
        </div>

        <div className="space-y-5">
          {v.words.length > 0 && (
            <Panel id="fix-one-part" title="Fix one part" sub="Click a word, then another to pick everything between them. Only that part is made again and stitched back in. You check it before it replaces anything.">
              <p className="flex flex-wrap gap-x-1 gap-y-1 text-[15px] leading-7">
                {v.words.map((w, i) => <button key={i} type="button" disabled={!canFix} aria-pressed={from !== null && to !== null && i >= from && i <= to} title={clock(w.start)} onClick={() => pickWord(i)} className={`rounded px-1 ${from !== null && to !== null && i >= from && i <= to ? "bg-amber-200 text-black" : canFix ? "hover:bg-muted" : ""}`}>{w.text}</button>)}
              </p>
              {!canFix && <p className="mt-3 text-[12px] text-muted-foreground">{!canManage ? "You can read this video but not change it." : waiting ? "Answer the fix above first: keep it or go back." : !finishing ? "This server has no ffmpeg, which cuts and stitches videos, so a part can't be fixed here." : "A part can be fixed once the video is ready."}</p>}
              {canFix && picked.length > 0 && from !== null && to !== null && (
                <div className="mt-4 rounded-lg border p-4">
                  <div className="text-[13px] font-medium">You picked "{picked.map((w) => w.text).join(" ")}" · {clock(picked[0].start)} to {clock(picked[picked.length - 1].end)}</div>
                  <div role="group" aria-label="What is wrong" className="mt-3 flex flex-wrap gap-2">
                    {([["said", "Said wrong"], ["change", "Change the words"], ["remove", "Remove it"], ["look", "Looks wrong"]] as const).map(([id, label]) => <button key={id} type="button" onClick={() => setWhat(id)} aria-pressed={what === id} className={`rounded-md border px-3 py-1.5 text-[13px] ${what === id ? "border-foreground bg-foreground text-background" : ""}`}>{label}</button>)}
                  </div>
                  {what === "said" && <input aria-label="How it should sound" value={say} maxLength={1000} onChange={(e) => setSay(e.target.value)} placeholder='How it should sound, spelled as you say it, such as "north-peek"' className={`${field} mt-3 w-full`} />}
                  {what === "change" && <input aria-label="New words" value={text} maxLength={1000} onChange={(e) => setText(e.target.value)} placeholder="The words to say instead" className={`${field} mt-3 w-full`} />}
                  <textarea aria-label="What is wrong, in your own words" rows={2} maxLength={1000} value={wrong} onChange={(e) => setWrong(e.target.value)} placeholder="Say what is wrong, in your own words. It is kept, and told to the AI." className={`${field} mt-3 w-full`} />
                  <div className="mt-3 flex flex-wrap items-center justify-between gap-3">
                    <span className="text-[12px] text-muted-foreground">{what === "remove" ? "Cutting words out costs nothing." : "Makes only this part again: a short voice and a few seconds of video, on your accounts."}</span>
                    <span className="flex gap-2"><Button variant="ghost" onClick={() => { setFrom(null); setTo(null); }}>Cancel</Button><Button disabled={busy || (what === "change" && !text.trim())} onClick={fix}>Fix this part</Button></span>
                  </div>
                </div>
              )}
            </Panel>
          )}

          {v.versions.length > 0 && (
            <Panel title="Versions" sub="Every version is kept. Going back to one keeps the one it replaces too.">
              <table className="w-full">
                <thead><tr className="border-b"><th className={th}>Version</th><th className={th}>What it is</th><th className={th}>Made</th><th className={th} /></tr></thead>
                <tbody>{v.versions.map((ver) => (
                  <tr key={ver.number} className="border-b last:border-0">
                    <td className={`${td} font-mono`}>{ver.number}</td>
                    <td className={td}><Badge tone={ver.state === "current" ? "green" : ver.state === "waiting" ? "amber" : "grey"}>{{ current: "The video now", waiting: "Fix waiting for you", before: "Earlier version", gone_back: "Fix you turned down" }[ver.state]}</Badge>{(ver.reason || ver.note) && <span className="ml-2 text-muted-foreground">{[ver.reason, ver.note && `"${ver.note}"`].filter(Boolean).join(": ")}</span>}</td>
                    <td className={`${td} whitespace-nowrap text-muted-foreground`}>{ago(ver.created_at)}</td>
                    <td className={`${td} whitespace-nowrap text-right`}>
                      {ver.final_asset_id && <a href={fileUrl(brand.id, ver.final_asset_id)} target="_blank" rel="noreferrer"><Button variant="ghost">See it</Button></a>}
                      {canManage && v.status === "ready" && !waiting && ver.state !== "current" && ver.final_asset_id && <Button variant="ghost" disabled={busy} onClick={() => restore(ver)}>Go back to this version</Button>}
                    </td>
                  </tr>
                ))}</tbody>
              </table>
            </Panel>
          )}

          <Panel title="Everything that happened to this video" sub="What was asked, what was said about it and who said it. Nothing is ever thrown away.">
            {history.length === 0 ? <p className="text-[13px] text-muted-foreground">Nothing yet.</p> : (
              <table className="w-full">
                <thead><tr className="border-b"><th className={th}>When</th><th className={th}>Who</th><th className={th}>What</th><th className={th}>Result</th></tr></thead>
                <tbody>{history.map((h) => {
                  const line = historyLine(h);
                  return <tr key={h.id} className="border-b last:border-0"><td className={`${td} whitespace-nowrap text-muted-foreground`}>{ago(h.created_at)}</td><td className={`${td} whitespace-nowrap`}>{h.actor_name ?? (h.actor_id ? "Someone" : "Outflare")}</td><td className={td}>{line.what}</td><td className={td}>{line.result && <Badge tone={line.tone}>{line.result}</Badge>}</td></tr>;
                })}</tbody>
              </table>
            )}
          </Panel>
        </div>
      </div>
    </div>
  );
}

// Variations: the family side by side to rate, then one thing to change
// in the next ones, and what the brand already taught.
function Variations({ brand, v, canManage, back, open, reloadList, onConnectors }: { brand: Brand; v: BrandVideo; canManage: boolean; back: () => void; open: (id: string) => void; reloadList: () => Promise<void>; onConnectors?: () => void }) {
  const root = v.variation_of ?? v.id;
  const { voices, voiceError, pictures } = usePicks(brand.id);
  const [family, setFamily] = useState<BrandVideo[] | null>(null);
  const [lessons, setLessons] = useState<CreativeLesson[]>([]);
  const [change, setChange] = useState<Change>("first_line");
  const [lines, setLines] = useState(["", "", ""]);
  const [picks, setPicks] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [notThis, setNotThis] = useState<{ id: string; why: string } | null>(null);
  const load = useCallback(async () => {
    const [fam, les] = await Promise.all([api.GET("/v1/brands/{brand}/videos", { params: { path: { brand: brand.id }, query: { family: root } } }), api.GET("/v1/brands/{brand}/creative-lessons", { params: { path: { brand: brand.id } } })]);
    setFamily([...(fam.data?.data ?? [])].reverse());
    setLessons(les.data?.data ?? []);
  }, [brand.id, root]);
  useEffect(() => { void load(); }, [load]);
  const making = (family ?? []).some((x) => x.status === "working");
  useEffect(() => {
    if (!making) return;
    const t = setInterval(() => void load(), 5000);
    return () => clearInterval(t);
  }, [making, load]);

  const text = change === "first_line" || change === "direction";
  const values = (text ? lines.map((l) => l.trim()) : picks).filter(Boolean);
  const pick = (id: string) => setPicks((p) => (p.includes(id) ? p.filter((x) => x !== id) : p.length < 5 ? [...p, id] : p));
  const setKind = (c: Change) => { setChange(c); setPicks([]); };
  const make = async () => {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/videos/{video}/variations", { params: { path: { brand: brand.id, video: v.id } }, body: { change, values } });
    setBusy(false);
    if (error || !data) { toast(errorMessage(error), "error"); return; }
    toast(data.data.length === 1 ? "Making 1 variation." : `Making ${data.data.length} variations.`);
    setLines(["", "", ""]); setPicks([]);
    await Promise.all([load(), reloadList()]);
  };
  const rate = async (x: BrandVideo, rating: "" | "liked" | "not_this", reason = "") => {
    const { error } = await api.POST("/v1/brands/{brand}/videos/{video}/rating", { params: { path: { brand: brand.id, video: x.id } }, body: { rating, reason } });
    if (error) { toast(errorMessage(error), "error"); return; }
    setNotThis(null);
    await Promise.all([load(), reloadList()]);
  };

  // Lessons: what the brand said once and wants kept.
  const [kind, setLessonKind] = useState<"say" | "rule">("say");
  const [word, setWord] = useState("");
  const [say, setSay] = useState("");
  const [rule, setRule] = useState("");
  const teach = async () => {
    const body = kind === "say" ? { kind, word: word.trim(), say: say.trim() } : { kind, rule: rule.trim(), applies_to: "video" as const };
    const { error } = await api.POST("/v1/brands/{brand}/creative-lessons", { params: { path: { brand: brand.id } }, body: { ...body, source_type: "video", source_id: v.id } });
    if (error) { toast(errorMessage(error), "error"); return; }
    setWord(""); setSay(""); setRule("");
    toast("Remembered for every new video.");
    await load();
  };
  const forget = async (l: CreativeLesson) => {
    const { error } = await api.DELETE("/v1/brands/{brand}/creative-lessons/{lesson}", { params: { path: { brand: brand.id, lesson: l.id } } });
    if (error) { toast(errorMessage(error), "error"); return; }
    await load();
  };
  const diff = (x: BrandVideo) => {
    const c = x.variation_change as { change?: Change; value?: string };
    if (!x.variation_of || !c.change) return `First line: "${x.script.split(/(?<=[.!?])\s/)[0]}"`;
    const shown = c.change === "voice" ? voices?.find((vo) => vo.id === c.value)?.name ?? c.value : c.change === "scene" ? pictures.find((p) => p.id === c.value)?.name ?? "another picture" : `"${c.value}"`;
    return `${changeNames[c.change]}: ${shown}`;
  };
  const options: [Change, string][] = [
    ["first_line", "The first three seconds decide if people watch. Write one first line for each variation."],
    ["direction", "Same room and words, someone else saying them, or a different look."],
    ["scene", "Same person and words, in another place from the brand's files."],
    ["voice", "Same video plan, another voice from your ElevenLabs account."],
  ];
  const same = (["first_line", "direction", "scene", "voice"] as Change[]).filter((c) => c !== change).map((c) => ({ first_line: "The script's first line", direction: "Person and look", scene: "Scene picture", voice: "Voice" }[c]));

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <Back to={v.name || "The video"} onClick={back} />
          <h2 className="mt-1 text-xl font-medium">Variations of "{v.name || "this video"}"</h2>
          <p className="max-w-3xl text-[13px] text-muted-foreground">Change one thing at a time, so you can tell what made the difference. Everything else is copied from this video.</p>
        </div>
        {canManage && <div className="flex items-center gap-2"><Button variant="ghost" onClick={back}>Cancel</Button><Button disabled={busy || values.length === 0} onClick={make}>{values.length === 0 ? "Make variations" : values.length === 1 ? "Make 1 variation" : `Make ${values.length} variations`}</Button></div>}
      </div>

      <Panel title="Rate what you have" sub="Like marks what to make more of. Not this asks why, and keeps the reason.">
        {!family ? <Loading /> : (
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            {family.map((x) => (
              <div key={x.id} className={`rounded-lg border p-3 ${needsRating(x) ? "border-amber-400" : ""}`}>
                <Player brand={brand.id} asset={x.final_asset_id} label={x.status === "working" ? "Being made…" : x.status === "failed" ? "It was not made" : "No video"} />
                <div className="mt-2 flex flex-wrap items-center gap-2 text-[13px] font-medium">{x.variation_of ? x.name : "Original"} <Badge tone={state(x).tone}>{state(x).label}</Badge></div>
                <div className="text-[12px] text-muted-foreground">{diff(x)}</div>
                {x.status === "ready" && canManage && (notThis?.id === x.id ? (
                  <div className="mt-3 space-y-2">
                    <textarea aria-label="Why not" autoFocus rows={2} maxLength={500} value={notThis.why} onChange={(e) => setNotThis({ id: x.id, why: e.target.value })} placeholder="Why not? It is kept." className={`${field} w-full`} />
                    <div className="flex gap-2"><Button onClick={() => rate(x, "not_this", notThis.why.trim())}>Save</Button><Button variant="ghost" onClick={() => setNotThis(null)}>Cancel</Button></div>
                  </div>
                ) : x.rating === "" ? (
                  <div className="mt-3 flex flex-wrap gap-2"><Button onClick={() => rate(x, "liked")}>Like</Button><Button variant="ghost" onClick={() => setNotThis({ id: x.id, why: "" })}>Not this</Button><Button variant="ghost" onClick={() => open(x.id)}>Fix</Button></div>
                ) : (
                  <div className="mt-3 text-[12px]">{x.rating_reason && <>You said: "{x.rating_reason}". </>}<button type="button" className="underline" onClick={() => rate(x, "")}>Change my mind</button> · <button type="button" className="underline" onClick={() => open(x.id)}>Open</button></div>
                ))}
              </div>
            ))}
          </div>
        )}
      </Panel>

      <div className="grid gap-5 xl:grid-cols-[1fr_380px]">
        <Panel title="What should change in the next ones?" sub="Turn on one. Changing one thing at a time shows what worked.">
          <div className="space-y-3">{options.map(([c, hint]) => (
            <div key={c} className={`rounded-lg border p-4 ${change === c ? "border-foreground" : ""}`}>
              <label className="flex items-start gap-3"><Switch checked={change === c} onCheckedChange={() => setKind(c)} disabled={!canManage} aria-label={changeNames[c]} /><span><span className="block text-[13px] font-medium">{changeNames[c]} <span className="font-normal text-muted-foreground">{change === c ? "On" : "Off"}</span></span><span className="block text-[12px] text-muted-foreground">{hint}</span></span></label>
              {change === c && canManage && (
                <div className="mt-3 space-y-2 sm:pl-12">
                  {text && lines.map((l, i) => c === "first_line"
                    ? <input key={i} aria-label={`First line ${i + 1}`} value={l} maxLength={500} onChange={(e) => setLines(lines.map((x, j) => (j === i ? e.target.value : x)))} placeholder={i === 0 ? v.script.split(/(?<=[.!?])\s/)[0] : "Another first line"} className={`${field} w-full`} />
                    : <textarea key={i} aria-label={`Person and look ${i + 1}`} rows={2} value={l} maxLength={10000} onChange={(e) => setLines(lines.map((x, j) => (j === i ? e.target.value : x)))} placeholder={i === 0 ? v.direction || "A woman in her 40s, glasses, calm" : "Another person or look"} className={`${field} w-full`} />)}
                  {text && lines.length < 5 && <Button variant="ghost" onClick={() => setLines([...lines, ""])}>Add another</Button>}
                  {c === "scene" && <PicturePick pictures={pictures.filter((p) => p.id !== v.scene_asset_id)} picked={picks} toggle={pick} />}
                  {c === "voice" && (voices && voices.length > 0 ? (
                    <span className="flex flex-wrap gap-2">{voices.filter((vo) => vo.id !== v.settings.voice.voice).map((vo) => <button key={vo.id} type="button" aria-pressed={picks.includes(vo.id)} onClick={() => pick(vo.id)} className={`rounded-md border px-3 py-1.5 text-[13px] ${picks.includes(vo.id) ? "border-foreground bg-foreground text-background" : ""}`}>{vo.name}</button>)}</span>
                  ) : <span className="text-[13px] text-muted-foreground">{voices ? voiceError || "Your ElevenLabs account has no other voices." : "Reading your voices…"} {voiceError && onConnectors && <button type="button" className="underline" onClick={onConnectors}>Open Connectors</button>}</span>)}
                  {!text && <p className="text-[12px] text-muted-foreground">Pick up to 5: one variation for each.</p>}
                </div>
              )}
            </div>
          ))}</div>
        </Panel>
        <div className="space-y-5">
          <Panel title="Stays the same">
            <ul className="space-y-1 text-[13px]">{[...same, "The rest of the script", "AI models and finish"].map((x) => <li key={x}>✓ {x}</li>)}</ul>
          </Panel>
          <Panel title="What you have told us" sub="Used in every new video. How to say a word goes to every voice; a rule goes into every prompt.">
            {lessons.length === 0 ? <p className="text-[13px] text-muted-foreground">Nothing yet. Fixes you keep with "Remember this" land here.</p> : (
              <ul className="space-y-2 text-[13px]">{lessons.map((l) => <li key={l.id} className="flex justify-between gap-2"><span>{l.kind === "say" ? `"${l.word}" is said "${l.say}"` : l.rule}{l.kind === "rule" && l.applies_to !== "video" && <span className="text-muted-foreground"> ({l.applies_to === "all" ? "pictures and videos" : "pictures"})</span>}</span>{canManage && <button type="button" className="text-muted-foreground hover:text-foreground" aria-label={`Remove ${l.kind === "say" ? l.word : l.rule}`} onClick={() => forget(l)}>Remove</button>}</li>)}</ul>
            )}
            {canManage && (
              <div className="mt-4 space-y-2 border-t pt-4">
                <select aria-label="Kind of lesson" value={kind} onChange={(e) => setLessonKind(e.target.value as "say" | "rule")} className={`${field} w-full`}><option value="say">How to say a word</option><option value="rule">A rule for every video</option></select>
                {kind === "say" ? (
                  <span className="flex gap-2"><input aria-label="Word" value={word} maxLength={100} onChange={(e) => setWord(e.target.value)} placeholder="Northpeak" className={`${field} w-1/2`} /><input aria-label="Said as" value={say} maxLength={200} onChange={(e) => setSay(e.target.value)} placeholder="north-peek" className={`${field} w-1/2`} /></span>
                ) : <input aria-label="Rule" value={rule} maxLength={500} onChange={(e) => setRule(e.target.value)} placeholder="No logos on clothing" className={`${field} w-full`} />}
                <Button variant="ghost" disabled={kind === "say" ? !word.trim() || !say.trim() : !rule.trim()} onClick={teach}>Remember this</Button>
              </div>
            )}
          </Panel>
          <Panel title="Cost"><p className="text-[13px]">Each variation is a whole new video, paid on your own ElevenLabs and fal.ai accounts like the first one. Outflare adds nothing.</p></Panel>
        </div>
      </div>
    </div>
  );
}
