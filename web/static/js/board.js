/* Chonkboard board behaviour: drag to move, and live updates that never fight
   the hand that is dragging. No framework, no build step. */
(function () {
  "use strict";

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  /* A per-tab id. The server echoes a mutation to every subscriber except the
     one that caused it, so two tabs of the same account both stay live while
     nobody receives their own change twice. */
  var CLIENT_ID =
    (window.crypto && window.crypto.randomUUID && window.crypto.randomUUID()) ||
    String(Date.now()) + Math.random().toString(16).slice(2);

  var dragging = false;

  /* streamUp tracks whether the stream is currently up; awaitingFirstOpen
     distinguishes the initial connection from a reconnect. Without the second flag
     every page load would fetch the board twice — once to render it, once because the
     stream opened.

     Named streamUp rather than `live` because two functions below take a local
     `live` for the #live element, and a shadowed flag is a bug waiting for the next
     edit. */
  var streamUp = false;
  var awaitingFirstOpen = true;
  var deferred = [];

  function csrf() {
    var m = document.querySelector('meta[name="csrf-token"]');
    return m ? m.getAttribute("content") : "";
  }

  function toast(message, tone) {
    var host = document.getElementById("toasts");
    if (!host) return;
    var el = document.createElement("div");
    el.className =
      "pointer-events-auto rounded-card border px-3 py-2 text-sm shadow-pop " +
      (tone === "error"
        ? "border-danger/40 bg-surface text-danger"
        : "border-line bg-surface text-ink-2");
    el.setAttribute("role", tone === "error" ? "alert" : "status");
    el.textContent = message;
    host.appendChild(el);
    window.setTimeout(function () {
      el.remove();
    }, 6000);
  }

  function orderOf(container) {
    return Array.prototype.map.call(
      container.querySelectorAll("[data-card-uuid]"),
      function (el) {
        return el.getAttribute("data-card-uuid");
      }
    );
  }

  /* Point the SSE stream at this tab. The attribute is set here rather than in
     the template because only the client knows its own id. */
  function connectLive() {
    var live = document.getElementById("live");
    if (!live || live.dataset.connected === "1") return;
    var board = document.getElementById("board");
    if (!board) return;
    var project = board.getAttribute("data-project-uuid");
    live.setAttribute(
      "sse-connect",
      "/projects/" + project + "/events?client=" + encodeURIComponent(CLIENT_ID)
    );
    live.dataset.connected = "1";
    if (window.htmx) window.htmx.process(live);
  }

  function bindLanes() {
    var lanes = document.querySelectorAll("[data-lane-cards]");
    Array.prototype.forEach.call(lanes, function (lane) {
      if (lane.dataset.sortable === "1") return;
      lane.dataset.sortable = "1";
      new window.Sortable(lane, {
        group: "board",
        animation: REDUCED ? 0 : 150,
        easing: "cubic-bezier(0.165, 0.84, 0.44, 1)",
        ghostClass: "card-ghost",
        chosenClass: "card-chosen",
        dragClass: "card-drag",
        draggable: "[data-card-uuid]",
        /* A drag on a phone must not also scroll the lane rail. */
        delay: 120,
        delayOnTouchOnly: true,
        touchStartThreshold: 4,
        fallbackTolerance: 4,
        onStart: function () {
          dragging = true;
        },
        onEnd: function (evt) {
          dragging = false;
          flushDeferred();
          submitMove(evt);
        },
      });
    });
  }

  function submitMove(evt) {
    var card = evt.item.getAttribute("data-card-uuid");
    var to = evt.to;
    var from = evt.from;
    var sameLane = to === from;
    var movedWithinSamePosition = sameLane && evt.oldIndex === evt.newIndex;
    if (movedWithinSamePosition) return;

    var limit = parseInt(to.getAttribute("data-wip-limit") || "0", 10);
    var count = to.querySelectorAll("[data-card-uuid]").length;
    if (!sameLane && limit > 0 && count > limit) {
      toast("That lane is at its limit of " + limit + ".", "error");
      revert(from, to);
      return;
    }

    var body = new URLSearchParams();
    body.set("to_lane", to.getAttribute("data-lane-uuid"));
    orderOf(to).forEach(function (u) {
      body.append("to_order", u);
    });
    if (!sameLane) {
      body.set("from_lane", from.getAttribute("data-lane-uuid"));
      orderOf(from).forEach(function (u) {
        body.append("from_order", u);
      });
    }

    fetch("/cards/" + card + "/move", {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/x-www-form-urlencoded",
        "X-CSRF-Token": csrf(),
        "X-Client-Id": CLIENT_ID,
        "HX-Request": "true",
      },
      body: body.toString(),
    })
      .then(function (res) {
        if (res.status === 204) return null;
        return res.text().then(function (html) {
          if (html && window.htmx) {
            window.htmx.swap("#toasts", html, { swapStyle: "beforeend" });
          } else if (!res.ok) {
            toast("That move could not be saved.", "error");
          }
          if (!res.ok) revert(from, to);
          return null;
        });
      })
      .catch(function () {
        toast("Lost the connection — that move was not saved.", "error");
        revert(from, to);
      });
  }

  /* Ask the server what these lanes really contain rather than guessing at an
     undo. Whatever the database says wins. */
  function revert(from, to) {
    [from, to].forEach(function (lane) {
      if (!lane || !window.htmx) return;
      var uuid = lane.getAttribute("data-lane-uuid");
      window.htmx.ajax("GET", "/lanes/" + uuid + "/fragment", {
        target: "#lane-" + uuid,
        swap: "outerHTML",
      });
    });
  }

  /* An SSE update that lands mid-drag would pull the card out from under the
     pointer. Hold the payload, apply it on drop.

     The hook is htmx:sseBeforeMessage, not htmx:beforeSwap: the SSE extension
     calls htmx's swap directly rather than going through the request pipeline, so
     the usual swap events never fire for a stream message. */
  function applyLive(data) {
    var live = document.getElementById("live");
    if (!live || !window.htmx) return;
    /* Every payload we broadcast is made of hx-swap-oob elements, and htmx runs
       its out-of-band pass before the main swap — so "none" applies the update
       without touching the element we aim it at. */
    window.htmx.swap(live, data, { swapStyle: "none" });
  }

  function flushDeferred() {
    var queued = deferred.splice(0, deferred.length);
    queued.forEach(applyLive);
  }

  document.addEventListener("DOMContentLoaded", function () {
    connectLive();
    bindLanes();
  });

  /* htmx replaces the board on reconnect and on any structural change. */
  document.body.addEventListener("htmx:afterSwap", function () {
    connectLive();
    bindLanes();
  });

  document.body.addEventListener("htmx:sseBeforeMessage", function (e) {
    if (!dragging) return;
    e.preventDefault();
    deferred.push(e.detail.data);
  });

  /* An out-of-band swap brings in a whole lane, so its card container is a new
     element that no Sortable is bound to yet. */
  document.body.addEventListener("htmx:oobAfterSwap", bindLanes);

  document.body.addEventListener("htmx:sseError", function () {
    streamUp = false;
    toast("Live updates dropped. Reconnecting…");
  });

  /* On reconnect, re-fetch the whole board.

     EventSource retries on its own, but events sent while it was disconnected are
     simply gone — nothing replays them. A board that only resumed listening would
     sit quietly stale, which is worse than visibly broken because nobody notices.

     Skipped on the first open: the page was just rendered from the database, so
     there is no gap to close and a fetch here would double every page load. */
  document.body.addEventListener("htmx:sseOpen", function () {
    if (streamUp) return;
    streamUp = true;

    if (awaitingFirstOpen) {
      awaitingFirstOpen = false;
      return;
    }
    refetchBoard();
  });

  /* Ask the board to reload itself. The same path a board-dirty signal takes, so a
     reconnect and a structural change converge on one implementation. */
  function refetchBoard() {
    var board = document.getElementById("board");
    if (!board || !window.htmx) return;
    window.htmx.trigger(board, "board-dirty");
  }
})();
