// sub_scribe UI behavior — tiny, dependency-free vanilla JS.
// Two jobs: (1) live progress + activity via Server-Sent Events, and
// (2) drag-and-drop niceties for the cookie upload.

(function () {
  "use strict";

  document.addEventListener("DOMContentLoaded", function () {
    initTheme();
    initDropzone();
    initCutoff();
    initRetention();
    initEvents();
    initLiveRefresh();
    initDeleteConfirm();
  });

  // initDeleteConfirm routes every delete form through one confirmation dialog,
  // which states what is being removed and offers to delete the downloaded files
  // too. That option always starts unchecked: losing the records is recoverable,
  // losing the media is not.
  function initDeleteConfirm() {
    var dialog = document.getElementById("confirm-delete");
    if (!dialog || typeof dialog.showModal !== "function") {
      return; // no dialog support: forms submit normally, keeping the files
    }

    var pending = null;
    document.querySelectorAll("form[data-confirm-delete]").forEach(function (form) {
      form.addEventListener("submit", function (event) {
        if (form.dataset.confirmed === "true") {
          return; // second pass, after the user confirmed
        }
        event.preventDefault();
        pending = form;
        describeTarget(dialog, form);
        dialog.showModal();
      });
    });

    dialog.addEventListener("close", function () {
      var form = pending;
      pending = null;
      if (!form || dialog.returnValue !== "confirm") {
        return;
      }
      var wanted = document.getElementById("confirm-delete-files");
      var field = form.querySelector('input[name="delete_files"]');
      if (field) {
        field.value = wanted && wanted.checked ? "true" : "false";
      }
      form.dataset.confirmed = "true";
      form.submit();
    });
  }

  // describeTarget fills the dialog with the specific source being deleted, so
  // the confirmation names it rather than asking about "this item".
  function describeTarget(dialog, form) {
    var count = Number(form.dataset.fileCount) || 0;
    setText(dialog, "[data-confirm-name]", form.dataset.sourceName || "this source");
    setText(dialog, "[data-confirm-count]", String(count));
    setText(dialog, "[data-confirm-size]", form.dataset.fileSize || "0 B");

    var toggle = dialog.querySelector("[data-confirm-files]");
    var note = dialog.querySelector("[data-confirm-nofiles]");
    if (toggle) { toggle.hidden = count === 0; }
    if (note) { note.hidden = count > 0; }

    var checkbox = document.getElementById("confirm-delete-files");
    if (checkbox) { checkbox.checked = false; }
  }

  function setText(root, selector, value) {
    var node = root.querySelector(selector);
    if (node) { node.textContent = value; }
  }

  // initLiveRefresh keeps queue screens current without a manual reload. A page
  // opts in by marking its live region with data-live="<milliseconds>"; the
  // region's contents are swapped for a freshly fetched copy, so watching a
  // download is a passive activity rather than a refresh loop. Without JS the
  // page is simply static, and the Refresh button still works.
  function initLiveRefresh() {
    var region = document.querySelector("[data-live]");
    if (!region || typeof fetch !== "function") {
      return;
    }
    var interval = Number(region.getAttribute("data-live")) || 5000;
    setInterval(refreshLiveRegion, interval);
  }

  function refreshLiveRegion() {
    // Skip while the tab is hidden: nobody is watching, and it saves the server
    // a query per tab per tick.
    if (document.hidden) {
      return;
    }
    fetch(window.location.href, { credentials: "same-origin" })
      .then(function (response) {
        return response.ok ? response.text() : null;
      })
      .then(swapLiveRegion)
      .catch(function () { /* a dropped poll is not worth surfacing */ });
  }

  function swapLiveRegion(html) {
    var region = document.querySelector("[data-live]");
    if (!html || !region) {
      return;
    }
    var incoming = new DOMParser()
      .parseFromString(html, "text/html")
      .querySelector("[data-live]");
    if (!incoming || incoming.innerHTML === region.innerHTML) {
      return;
    }
    // Preserve where the user had scrolled to in a log panel, so a refresh never
    // yanks the view out from under them mid-read.
    var log = region.querySelector(".logs");
    var offset = log ? log.scrollTop : 0;
    var pinned = log ? log.scrollHeight - log.scrollTop - log.clientHeight < 40 : false;

    region.innerHTML = incoming.innerHTML;

    var newLog = region.querySelector(".logs");
    if (newLog) {
      newLog.scrollTop = pinned ? newLog.scrollHeight : offset;
    }
  }

  // initCutoff keeps the date field in step with the "published within"
  // dropdown. Choosing a period fills in and shows the date that period works
  // out to today — read-only, because the window is rolling and re-resolved on
  // every scan — while "Since a specific date" makes it editable and pins it.
  // Without JS the server renders the field's initial state, so the form still
  // works; only the live preview is lost.
  function initCutoff() {
    var select = document.querySelector("[data-cutoff-select]");
    var dateField = document.querySelector("[data-cutoff-date]");
    if (!select || !dateField) {
      return;
    }
    var input = dateField.querySelector("input[type=date]");
    var rollingNote = dateField.querySelector("[data-cutoff-rolling]");
    // What the date was when the page loaded, so switching to "specific date"
    // and back does not lose a value the user had already pinned.
    var pinned = input ? input.value : "";

    function sync() {
      var isCustom = select.value === "custom";
      var days = Number(select.value);

      dateField.hidden = select.value === "";
      if (rollingNote) { rollingNote.hidden = isCustom; }
      if (!input) { return; }

      input.readOnly = !isCustom;
      if (isCustom) {
        input.value = pinned;
      } else if (days > 0) {
        input.value = daysAgo(days);
      }
    }

    if (input) {
      input.addEventListener("change", function () {
        if (select.value === "custom") { pinned = input.value; }
      });
    }
    select.addEventListener("change", sync);
    sync();
  }

  // daysAgo returns the date that many days before today as YYYY-MM-DD, which is
  // the format a date input expects.
  function daysAgo(days) {
    var date = new Date();
    date.setDate(date.getDate() - days);
    var month = String(date.getMonth() + 1).padStart(2, "0");
    var day = String(date.getDate()).padStart(2, "0");
    return date.getFullYear() + "-" + month + "-" + day;
  }

  // initRetention reveals the day-count box only when "a specific number of days"
  // is chosen, mirroring how the cutoff dropdown behaves so the two read the same.
  function initRetention() {
    var select = document.querySelector("[data-retention-select]");
    var daysField = document.querySelector("[data-retention-days]");
    if (!select || !daysField) {
      return;
    }
    function sync() {
      daysField.hidden = select.value !== "custom";
    }
    select.addEventListener("change", sync);
    sync();
  }

  // initTheme wires the light/dark toggle and persists the choice so it survives
  // navigation. The initial theme is applied by an inline head script to avoid a
  // flash; this only handles the button press.
  function initTheme() {
    var root = document.documentElement;
    var btn = document.getElementById("theme");
    if (!btn) { return; }
    btn.addEventListener("click", function () {
      var current = root.getAttribute("data-theme");
      if (!current) {
        current = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
      }
      var next = current === "dark" ? "light" : "dark";
      root.setAttribute("data-theme", next);
      try { localStorage.setItem("theme", next); } catch (e) { /* private mode */ }
    });
  }

  // initDropzone wires the cookie upload box so a file can be dropped onto it
  // and its name is shown for confirmation. The plain file input still works
  // without any of this, keeping the flow accessible if JS is disabled.
  function initDropzone() {
    var zone = document.getElementById("dropzone");
    var input = document.getElementById("cookies");
    var filename = document.getElementById("dropzone-filename");
    if (!zone || !input) {
      return;
    }

    ["dragenter", "dragover"].forEach(function (name) {
      zone.addEventListener(name, function (event) {
        event.preventDefault();
        zone.classList.add("is-dragover");
      });
    });
    ["dragleave", "drop"].forEach(function (name) {
      zone.addEventListener(name, function (event) {
        event.preventDefault();
        zone.classList.remove("is-dragover");
      });
    });
    zone.addEventListener("drop", function (event) {
      if (event.dataTransfer && event.dataTransfer.files.length) {
        input.files = event.dataTransfer.files;
        showFilename(filename, input);
      }
    });
    input.addEventListener("change", function () {
      showFilename(filename, input);
    });
  }

  function showFilename(target, input) {
    if (target && input.files.length) {
      target.textContent = "Selected: " + input.files[0].name;
    }
  }

  // initEvents subscribes to the SSE stream mounted by the server and reflects
  // progress + activity into the page. The path is read from the body so the
  // server stays the single source of truth for the endpoint.
  function initEvents() {
    var path = document.body.getAttribute("data-events-path");
    if (!path || typeof EventSource === "undefined") {
      return;
    }
    var stream = new EventSource(path);
    stream.addEventListener("progress", function (event) {
      applyProgress(event.data);
    });
    stream.addEventListener("activity", function (event) {
      logActivity(event.data);
    });
    stream.onmessage = function (event) {
      logActivity(event.data);
    };
    // The browser auto-reconnects on error; no manual retry loop needed.

    // Close the stream the instant we navigate away. A persistent SSE connection
    // otherwise lingers (and can be frozen in the back/forward cache), so rapid
    // navigation would pile connections up against the browser's per-host limit
    // and stall the next page load. Closing on pagehide releases it immediately.
    window.addEventListener("pagehide", function () {
      stream.close();
    });
  }

  // applyProgress expects a JSON payload {"source": id, "percent": n} and moves
  // the matching progress bar. Malformed payloads are ignored, never thrown.
  function applyProgress(raw) {
    var data = safeParse(raw);
    if (!data || data.source === undefined) {
      return;
    }
    var bar = document.querySelector('[data-progress="' + data.source + '"]');
    if (!bar) {
      return;
    }
    var wrap = document.querySelector('[data-source="' + data.source + '"]');
    if (wrap) {
      wrap.hidden = false;
    }
    var percent = Number(data.percent) || 0;
    bar.value = percent;
    bar.textContent = percent + "%";
  }

  function logActivity(text) {
    var log = document.getElementById("activity-log");
    if (!log || !text) {
      return;
    }
    var item = document.createElement("li");
    item.textContent = text;
    log.insertBefore(item, log.firstChild);
    while (log.childNodes.length > 20) {
      log.removeChild(log.lastChild);
    }
  }

  function safeParse(raw) {
    try {
      return JSON.parse(raw);
    } catch (err) {
      return null;
    }
  }
})();
