/* Fold every top-level h2 section of a page into a native <details open>.
   Runs after each Material instant navigation (document$). */
(function () {
  "use strict";

  var STOP = ".md-source-file, .md-feedback, .md-content__button";

  function foldPage() {
    var root = document.querySelector(".md-content__inner.md-typeset");
    if (!root || root.hasAttribute("data-fold-done")) return;
    root.setAttribute("data-fold-done", "");
    var h2s = Array.prototype.filter.call(root.children, function (el) {
      return el.tagName === "H2";
    });
    if (h2s.length < 2) return;
    h2s.forEach(function (h2) {
      var details = document.createElement("details");
      details.className = "claustrum-fold";
      details.open = true;
      var summary = document.createElement("summary");
      var body = document.createElement("div");
      body.className = "claustrum-fold__body";
      var next = h2.nextElementSibling;
      root.insertBefore(details, h2);
      summary.appendChild(h2);
      details.appendChild(summary);
      details.appendChild(body);
      while (next && next.tagName !== "H2" && !next.matches(STOP)) {
        var after = next.nextElementSibling;
        body.appendChild(next);
        next = after;
      }
    });
    revealHash(false);
  }

  function targetFor(hash) {
    if (!hash || hash.length < 2) return null;
    var id = hash.slice(1);
    try { id = decodeURIComponent(id); } catch (e) { /* keep raw id */ }
    return document.getElementById(id);
  }

  /* Open every fold that holds the target. Returns true when one opened. */
  function openAround(target) {
    var opened = false;
    for (var el = target; el; el = el.parentElement) {
      if (el.matches && el.matches("details.claustrum-fold") && !el.open) {
        el.open = true;
        opened = true;
      }
    }
    return opened;
  }

  function revealHash(scroll) {
    var target = targetFor(location.hash);
    if (target && openAround(target) && scroll !== false) target.scrollIntoView();
    else if (target && scroll === false && target.closest("details.claustrum-fold")) {
      target.scrollIntoView();
    }
  }

  /* Links to an anchor on this page open the section before the jump. */
  document.addEventListener("click", function (e) {
    var a = e.target.closest && e.target.closest("a[href*='#']");
    if (!a || a.origin !== location.origin || a.pathname !== location.pathname) return;
    var target = targetFor(a.hash);
    if (target) openAround(target);
  }, true);

  window.addEventListener("hashchange", function () { revealHash(true); });

  /* Closed details hide their text when printing, so open all and restore. */
  var closedBeforePrint = [];
  window.addEventListener("beforeprint", function () {
    closedBeforePrint = Array.prototype.filter.call(
      document.querySelectorAll("details.claustrum-fold"),
      function (d) { return !d.open; });
    closedBeforePrint.forEach(function (d) { d.open = true; });
  });
  window.addEventListener("afterprint", function () {
    closedBeforePrint.forEach(function (d) { d.open = false; });
    closedBeforePrint = [];
  });

  if (window.document$ && window.document$.subscribe) {
    window.document$.subscribe(foldPage);
  } else if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", foldPage);
  } else {
    foldPage();
  }
})();
