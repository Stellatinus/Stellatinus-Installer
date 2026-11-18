(function () {
  var dark = window.matchMedia("(prefers-color-scheme: dark)");
  var palettes = null;
  function apply() {
    if (!palettes) return;
    var p = dark.matches ? palettes.dark : palettes.light, s = document.documentElement.style;
    s.setProperty("--accent", p.fill); s.setProperty("--accent-ink", p.ink);
    s.setProperty("--accent-text", p.text); s.setProperty("--soft", p.soft);
  }
  dark.addEventListener("change", apply);
  function post(url, body) {
    return fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body || {}) });
  }
  fetch("/api/manifest").then(function (r) { return r.json(); }).then(function (m) {
    document.getElementById("app").textContent = m.name;
    document.title = m.name + " setup spike";
    palettes = m.palettes; apply();
    document.getElementById("paths").textContent = Object.keys(m.paths).map(function (k) { return k + ": " + m.paths[k]; }).join("\n");
  });
  document.getElementById("run").addEventListener("click", function () {
    post("/api/checks").then(function (r) { return r.json(); }).then(function (list) {
      var ul = document.getElementById("res"); ul.textContent = "";
      list.forEach(function (c) {
        var li = document.createElement("li"); li.className = c.status;
        var b = document.createElement("b"); b.textContent = c.title;
        var s = document.createElement("small"); s.textContent = c.detail;
        li.appendChild(b); li.appendChild(s); ul.appendChild(li);
      });
    });
  });
  document.getElementById("quit").addEventListener("click", function () {
    post("/api/quit").then(function () { document.body.textContent = "You can close this window."; });
  });
})();
