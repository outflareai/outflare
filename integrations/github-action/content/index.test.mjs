// node --test integrations/github-action/content/index.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { filePath, markdownFile, pageURL, prBody, revisionIn } from "./index.mjs";

const item = {
  id: "5d0c7b52-1d1e-4a52-9a49-3b0f3f6f2a10",
  title: 'Northpeak vs "Ridgeline"',
  slug: "northpeak-vs-ridgeline",
  target: "posts",
  language: "en-AU",
  body: "## Pricing\n\nNorthpeak bills **per seat**.\n",
  excerpt: "A side-by-side look.",
  tags: ["comparison"],
  revision: 3,
  approved_by: "Ana",
  placement: { type: "comparison", collection: "Blog Posts", path: "/compare/northpeak-vs-ridgeline", template_hint: "/compare/northpeak-vs-summitly" },
  seo: { meta_title: "Northpeak vs Ridgeline", primary_keyword: "northpeak vs ridgeline", schema: [{ "@type": "Article" }] },
  media: [{ role: "featured", url: "https://cdn.northpeak.example/vs.webp", alt: "Two dashboards" }],
  links: { add_links_from: [{ page: "/blog/choosing-a-crm", anchor: "compare them", after_text: "teams this size" }] },
  brief: { goal: "search", check_before_live: ["Ridgeline's price"] },
};

test("paths stay inside the repository", () => {
  assert.equal(filePath("content/{target}/{slug}.md", item), "content/posts/northpeak-vs-ridgeline.md");
  assert.equal(filePath("src/{collection}/{language}/{slug}.mdx", item), "src/blog-posts/en-au/northpeak-vs-ridgeline.mdx");
  assert.equal(filePath("blog/{slug}.md", { ...item, slug: "" }), `blog/${item.id}.md`);
  assert.equal(filePath("blog/{slug}.md", { ...item, slug: "../../etc/passwd" }), "blog/..-..-etc-passwd.md");
  for (const bad of ["/abs/{slug}.md", "../{slug}.md", ".github/workflows/{slug}.yml", "a/.git/{slug}", "{collection}/x.md"]) {
    assert.throws(() => filePath(bad, { ...item, placement: {} }), /isn't a file/, bad);
  }
});

test("page addresses", () => {
  assert.equal(pageURL("https://northpeak.example", "/blog/{slug}/", item), "https://northpeak.example/blog/northpeak-vs-ridgeline/");
  assert.equal(pageURL("https://northpeak.example/", "{path}", item), "https://northpeak.example/compare/northpeak-vs-ridgeline");
  assert.equal(pageURL("https://northpeak.example", "{path}", { ...item, placement: {} }), "");
  assert.equal(pageURL("", "/{slug}/", item), "");
});

test("the file carries its revision", () => {
  const f = markdownFile(item);
  assert.match(f, /^---\ntitle: "Northpeak vs \\"Ridgeline\\""\n/);
  assert.match(f, /\nlang: "en-AU"\n/);
  assert.match(f, /\nimage: "https:\/\/cdn.northpeak.example\/vs.webp"\nimage_alt: "Two dashboards"\n/);
  assert.match(f, /\n---\n\n## Pricing\n\nNorthpeak bills \*\*per seat\*\*.\n$/);
  assert.equal(revisionIn(f), 3);
  assert.equal(revisionIn("# no front matter"), -1);
  assert.equal(revisionIn(null), -1);
});

test("the pull request says what to check", () => {
  const b = prBody(item, "https://northpeak.example/compare/northpeak-vs-ridgeline");
  assert.match(b, /Approved in Outflare by Ana\. Revision 3\./);
  assert.match(b, /- \[ \] Ridgeline's price/);
  assert.match(b, /- \[ \] \/blog\/choosing-a-crm: link "compare them" after "teams this size"/);
  assert.match(b, /Follow the layout of \/compare\/northpeak-vs-summitly/);
  assert.match(b, /"@type": "Article"/);
});
