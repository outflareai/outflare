// The newest releases from CHANGELOG.md, read at build time (vite.config.ts).
declare module "virtual:outflare-changelog" {
  export type ReleaseChange = { kind: string; text: string };
  export type Release = { version: string; date: string; changes: ReleaseChange[] };
  const releases: Release[];
  export default releases;
}
