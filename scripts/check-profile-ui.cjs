const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('cmd/helper/main.go', 'utf8');
const script = source.match(/<script>([\s\S]*?)<\/script>/)[1];
new vm.Script(script); // Check the entire inline script, not just the renderer.
class Element {
  constructor(tag) { this.tag = tag; this.children = []; this.style = {}; this.textContent = ''; }
  appendChild(child) { this.children.push(child); }
  replaceChildren() { this.children = []; }
}
const body = new Element('tbody');
const sandbox = {Intl, Date, document:{getElementById:()=>body, createElement:tag=>new Element(tag)}, text:()=>{}};
vm.createContext(sandbox);
vm.runInContext(script.slice(script.indexOf('function countryName('), script.indexOf('async function busy(')), sandbox);
const profile = {id:'a', name:'<script>not HTML</script>', host:'server.example', port:443, scheme:'vless', supported:true};
sandbox.renderProfiles([profile], '', '', {a:{status:'healthy', server_ips:['192.0.2.1','2001:db8::1'], exit_ip:'203.0.113.2', exit_country:'DE', ttfb_ms:123}});
let cells = body.children[0].children;
assert.equal(cells.length, 10);
assert.equal(cells[0].textContent, profile.name);
assert.equal(cells[2].textContent, 'vless/raw');
assert.equal(cells[3].textContent, '192.0.2.1, 2001:db8::1');
assert.equal(cells[4].textContent, '203.0.113.2');
assert.match(cells[5].textContent, /DE/);
assert.match(cells[8].textContent, /123/);
sandbox.renderProfiles([profile], '', '', {a:{status:'healthy', ttfb_ms:100}});
assert.equal(body.children[0].children[4].textContent, '—');
sandbox.renderProfiles([profile], '', '', {a:{status:'healthy', identity_error:'Exit identity request failed'}});
assert.equal(body.children[0].children[4].title, 'Exit identity request failed');
sandbox.renderProfiles([], '', '', {});
assert.equal(body.children[0].children[0].colSpan, 10);
console.log('Profile UI checks passed');
