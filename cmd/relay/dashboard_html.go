package main

// dashboardHTML is the self-contained status page. It polls /api/status (same
// origin, so the mt_admin cookie authenticates it) and renders the relay,
// agents and live sessions. No external assets or frameworks.
const dashboardHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>MiniTunnel · relay status</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin:0; font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
         background:#0d1117; color:#e6edf3; }
  header { padding:18px 24px; border-bottom:1px solid #21262d; display:flex; align-items:center; gap:16px; flex-wrap:wrap; }
  h1 { font-size:18px; margin:0; font-weight:600; }
  .dot { width:9px; height:9px; border-radius:50%; display:inline-block; margin-right:6px; }
  .ok { background:#2ea043; } .warn { background:#d29922; } .bad { background:#f85149; }
  .meta { color:#8b949e; font-size:13px; }
  .meta b { color:#e6edf3; font-weight:600; }
  main { padding:24px; max-width:1100px; margin:0 auto; }
  .cards { display:flex; gap:14px; flex-wrap:wrap; margin-bottom:24px; }
  .card { background:#161b22; border:1px solid #21262d; border-radius:10px; padding:14px 18px; min-width:130px; }
  .card .n { font-size:26px; font-weight:700; }
  .card .l { color:#8b949e; font-size:12px; text-transform:uppercase; letter-spacing:.04em; }
  h2 { font-size:14px; color:#8b949e; text-transform:uppercase; letter-spacing:.04em; margin:24px 0 10px; }
  table { width:100%; border-collapse:collapse; background:#161b22; border:1px solid #21262d; border-radius:10px; overflow:hidden; }
  th,td { text-align:left; padding:10px 14px; border-bottom:1px solid #21262d; font-variant-numeric:tabular-nums; }
  th { color:#8b949e; font-weight:600; font-size:12px; text-transform:uppercase; letter-spacing:.03em; }
  tr:last-child td { border-bottom:none; }
  td.mono, .mono { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; }
  .empty { color:#8b949e; padding:18px 14px; }
  .pill { padding:2px 8px; border-radius:20px; font-size:12px; background:#21262d; }
  .pill.live { background:#1f6f3f; color:#d6ffe2; }
  .right { margin-left:auto; display:flex; align-items:center; gap:12px; }
  button { background:#21262d; color:#e6edf3; border:1px solid #30363d; border-radius:6px; padding:6px 12px; cursor:pointer; }
  button:hover { background:#30363d; }
  #err { color:#f85149; padding:10px 24px; display:none; }
</style>
</head>
<body>
<header>
  <h1>MiniTunnel <span class="meta">relay status</span></h1>
  <span id="status" class="meta"><span class="dot warn"></span>connecting…</span>
  <div class="right">
    <label class="meta"><input type="checkbox" id="auto" checked> auto-refresh</label>
    <button onclick="load()">Refresh</button>
  </div>
</header>
<div id="err"></div>
<main>
  <div class="cards">
    <div class="card"><div class="n" id="c-agents">–</div><div class="l">Agents online</div></div>
    <div class="card"><div class="n" id="c-sessions">–</div><div class="l">Live sessions</div></div>
    <div class="card"><div class="n" id="c-uptime">–</div><div class="l">Relay uptime</div></div>
    <div class="card"><div class="n mono" id="c-addr">–</div><div class="l">Listen addr</div></div>
  </div>

  <h2>Agents</h2>
  <table>
    <thead><tr><th>ID</th><th>Remote address</th><th>Connected</th><th>Last heartbeat</th><th>Sessions</th></tr></thead>
    <tbody id="agents"></tbody>
  </table>

  <h2>Live sessions</h2>
  <table>
    <thead><tr><th>Session</th><th>Agent</th><th>Target port</th><th>Client</th><th>Duration</th></tr></thead>
    <tbody id="sessions"></tbody>
  </table>
</main>

<script>
var BASE = "__PREFIX__";  // URL path prefix injected by the relay ("" or "/foo")
function dur(s){ s=Math.max(0,s|0);
  var d=Math.floor(s/86400); s%=86400;
  var h=Math.floor(s/3600); s%=3600;
  var m=Math.floor(s/60); var ss=s%60;
  if(d) return d+"d "+h+"h"; if(h) return h+"h "+m+"m"; if(m) return m+"m "+ss+"s"; return ss+"s";
}
function esc(t){ var e=document.createElement("span"); e.textContent=t==null?"":String(t); return e.innerHTML; }
function setStatus(cls,txt){ document.getElementById("status").innerHTML='<span class="dot '+cls+'"></span>'+esc(txt); }

async function load(){
  try{
    var res = await fetch(BASE+"/api/status",{credentials:"same-origin",cache:"no-store"});
    if(res.status===401){ setStatus("bad","unauthorized — open this page with ?token=…"); return; }
    if(res.status===503){ setStatus("bad","dashboard disabled — set MINITUNNEL_ADMIN_TOKEN"); return; }
    if(!res.ok){ throw new Error("HTTP "+res.status); }
    var d = await res.json();
    document.getElementById("err").style.display="none";

    document.getElementById("c-agents").textContent   = d.agents.length;
    document.getElementById("c-sessions").textContent = d.sessions.length;
    document.getElementById("c-uptime").textContent   = dur(d.uptime_sec);
    document.getElementById("c-addr").textContent     = d.listen_addr;

    var ab = document.getElementById("agents");
    if(!d.agents.length){ ab.innerHTML='<tr><td colspan="5" class="empty">No agents connected.</td></tr>'; }
    else { ab.innerHTML = d.agents.map(function(a){
      var stale = a.last_seen_sec > 90;
      return "<tr><td class='mono'>"+esc(a.id)+"</td>"+
        "<td class='mono'>"+esc(a.remote_addr)+"</td>"+
        "<td>"+dur(a.connected_sec)+" ago</td>"+
        "<td><span class='dot "+(stale?"bad":"ok")+"'></span>"+dur(a.last_seen_sec)+" ago</td>"+
        "<td>"+a.active_sessions+"</td></tr>";
    }).join(""); }

    var sb = document.getElementById("sessions");
    if(!d.sessions.length){ sb.innerHTML='<tr><td colspan="5" class="empty">No active sessions.</td></tr>'; }
    else { sb.innerHTML = d.sessions.map(function(s){
      return "<tr><td class='mono'>"+esc(s.id.slice(0,12))+"…</td>"+
        "<td class='mono'>"+esc(s.agent_id)+"</td>"+
        "<td><span class='pill live'>:"+s.target_port+"</span></td>"+
        "<td class='mono'>"+esc(s.client_addr)+"</td>"+
        "<td>"+dur(s.duration_sec)+"</td></tr>";
    }).join(""); }

    setStatus("ok","updated "+new Date().toLocaleTimeString());
  }catch(e){
    setStatus("bad","error");
    var el=document.getElementById("err"); el.style.display="block"; el.textContent="Failed to load status: "+e.message;
  }
}

var timer=null;
function tick(){ if(document.getElementById("auto").checked) load(); }
document.getElementById("auto").addEventListener("change",function(){ if(this.checked) load(); });
load();
setInterval(tick, 3000);
</script>
</body>
</html>`
