// Runs the website script's secret hiding (the block between the
// secrets:start and secrets:end marks in site.js) over the JSON given as
// the second argument: {"texts": [...], "addresses": [...], "event": {...}}.
import fs from "node:fs";
const src = fs.readFileSync(process.argv[2], "utf8");
const block = src.slice(src.indexOf("// secrets:start"), src.indexOf("// secrets:end"));
const f = new Function(block + "; return { hideSecrets, hideInAddress, hideInEvent, setShapes };")();
const input = JSON.parse(process.argv[3]);
f.setShapes(input.shapes || []);
f.hideInEvent(input.event);
console.log(JSON.stringify({ texts: input.texts.map(f.hideSecrets), addresses: input.addresses.map(f.hideInAddress), event: input.event }));
