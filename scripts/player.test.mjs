import assert from "node:assert/strict";
import {readFile} from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../internal/assets/static/player.js", import.meta.url), "utf8");
const appSource = await readFile(new URL("../internal/assets/static/app.js", import.meta.url), "utf8");

function fixture(context, kind = "live") {
    context.mock.timers.enable({apis: ["setTimeout"]});
    const previous = Object.fromEntries(["HTMLElement", "customElements", "window", "document", "navigator", "playNext"].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
    context.after(() => {
        player.pause();
        player.unbindAudioEvents();
        for (const [key, descriptor] of Object.entries(previous)) {
            if (descriptor) Object.defineProperty(globalThis, key, descriptor);
            else delete globalThis[key];
        }
    });
    class Element extends EventTarget {
        attributes = new Map();
        dataset = {};
        style = {};
        classList = {toggle() {}};
        isConnected = true;
        querySelector() { return null; }
        getAttribute(name) { return this.attributes.get(name) ?? null; }
        setAttribute(name, value) {
            const old = this.getAttribute(name);
            this.attributes.set(name, String(value));
            this.attributeChangedCallback?.(name, old, String(value));
        }
        removeAttribute(name) { this.attributes.delete(name); }
    }
    globalThis.HTMLElement = Element;
    let Player;
    globalThis.customElements = {define: (_, Constructor) => { Player = Constructor; }};
    globalThis.window = Object.assign(new EventTarget(), {
        setTimeout: (...args) => setTimeout(...args), clearTimeout: id => clearTimeout(id),
        matchMedia: () => ({matches: false}), cancelAnimationFrame() {},
    });
    globalThis.document = Object.assign(new EventTarget(), {visibilityState: "visible"});
    Object.defineProperty(globalThis, "navigator", {configurable: true, value: {onLine: true}});
    let nextCalls = 0;
    globalThis.playNext = () => { nextCalls++; };
    vm.runInThisContext(`(() => { ${source.replace(/^import .*;\n/gm, "")} })()`);
    const player = new Player();
    player.setAttribute("data-kind", kind);
    player.setAttribute("src", "/radio/1/audio/");
    player.dataset = {connectingLabel: "Connecting", stoppedLabel: "Stopped", playingLabel: "Playing", unavailableLabel: "Unavailable"};
    const audio = new Element();
    Object.assign(audio, {paused: true, ended: false, duration: Infinity, currentTime: 120, readyState: 4, loads: 0, plays: 0});
    audio.load = () => { audio.loads++; audio.ended = false; };
    audio.play = () => { audio.plays++; audio.paused = false; return Promise.resolve(); };
    audio.pause = () => { audio.paused = true; audio.dispatchEvent(new Event("pause")); };
    player.audioEl = audio;
    for (const name of ["playButtonEl", "playIconEl", "nextButtonEl", "progressBarEl", "progressEl", "elapsedEl", "totalEl", "liveIndicatorEl", "liveStatusEl"]) player[name] = new Element();
    player.oscilloscope = {connect() {}, disconnect() {}, prepare() {}, stop() {}, start() {}};
    player.bindAudioEvents();
    player.updateSourceControls();
    return {player, audio, nextCalls: () => nextCalls};
}

test("live playback hides seek and next controls even with a finite reported duration", context => {
    const {player, audio, nextCalls} = fixture(context);
    audio.duration = 300;
    assert.equal(player.progressBarEl.hidden, true);
    assert.equal(player.progressBarEl.tabIndex, -1);
    assert.equal(player.nextButtonEl.disabled, true);
    assert.equal(player.liveIndicatorEl.hidden, false);
    player.skipTo(0);
    player.seekBySeconds(-10);
    player.onNextButtonClick();
    assert.equal(audio.currentTime, 120);
    assert.equal(nextCalls(), 0);
});

test("an ended broadcast reconnects to live without seeking or advancing the queue", context => {
    const {player, audio, nextCalls} = fixture(context);
    player.play();
    audio.ended = true;
    audio.dispatchEvent(new Event("ended"));
    context.mock.timers.tick(2000);
    assert.equal(audio.plays, 2);
    assert.equal(audio.currentTime, 120);
    assert.equal(nextCalls(), 0);
    assert.equal(player.playbackRequested, true);
});

test("Stop releases the stream and cancels recovery; Play opens a new live connection", context => {
    const {player, audio} = fixture(context);
    player.play();
    audio.dispatchEvent(new Event("error"));
    const plays = audio.plays;
    player.pause();
    assert.equal(audio.getAttribute("src"), null);
    assert.equal(player.liveStatusEl.textContent, "Stopped");
    context.mock.timers.tick(60000);
    assert.equal(audio.plays, plays);
    player.play();
    assert.equal(audio.getAttribute("src"), "/radio/1/audio/");
    assert.equal(audio.plays, plays + 1);
});

test("a sustained outage backs off then offers a manual retry", context => {
    const {player, audio} = fixture(context);
    player.play();
    audio.dispatchEvent(new Event("error"));
    for (const delay of [2000, 4000, 8000, 16000, 30000, 30000, 30000]) context.mock.timers.tick(delay);
    assert.equal(player.playbackRequested, false);
    assert.equal(player.liveStatusEl.textContent, "Unavailable");
    assert.equal(audio.getAttribute("src"), null);
    player.play();
    assert.equal(player.recoveryAttempts, 0);
    assert.equal(player.playbackRequested, true);
});

test("offline recovery waits for connectivity and never opens another connection", context => {
    const {player, audio} = fixture(context);
    player.play();
    navigator.onLine = false;
    audio.dispatchEvent(new Event("stalled"));
    context.mock.timers.tick(2000);
    context.mock.timers.tick(5000);
    assert.equal(audio.plays, 1);
    navigator.onLine = true;
    player.onEnvironmentResume();
    context.mock.timers.tick(5000);
    assert.equal(audio.plays, 2);
});

test("autoplay denial stops recovery until another explicit Play", async context => {
    const {player, audio} = fixture(context);
    audio.play = () => Promise.reject(Object.assign(new Error("gesture required"), {name: "NotAllowedError"}));
    player.play();
    await Promise.resolve();
    assert.equal(player.playbackRequested, false);
    assert.equal(player.recoveryTimer, null);
    assert.equal(player.liveStatusEl.textContent, "Stopped");
});

test("switching to a library track cancels a late live play failure", async context => {
    const {player, audio} = fixture(context);
    let fail;
    audio.play = () => new Promise((_, reject) => { fail = reject; });
    player.play();
    player.pause();
    player.setAttribute("data-kind", "track");
    player.src = "/stream/1/";
    fail(new Error("old stream failed"));
    await Promise.resolve();
    context.mock.timers.tick(60000);
    assert.equal(player.recoveryTimer, null);
    assert.equal(player.nextButtonEl.disabled, false);
    assert.equal(player.progressBarEl.hidden, false);
});

test("library track recovery still restores its timestamp and advances on end", context => {
    const {player, audio, nextCalls} = fixture(context, "track");
    audio.setAttribute("src", "/stream/1/");
    audio.duration = 300;
    player.play();
    audio.dispatchEvent(new Event("error"));
    context.mock.timers.tick(2000);
    audio.currentTime = 0;
    audio.dispatchEvent(new Event("loadedmetadata"));
    assert.equal(audio.currentTime, 120);
    audio.dispatchEvent(new Event("ended"));
    assert.equal(nextCalls(), 1);
});

test("replacing audio transfers listeners and unbinding removes audio, control, and environment listeners", context => {
    const {player, audio, nextCalls} = fixture(context, "track");
    const replacement = new audio.constructor();
    Object.assign(replacement, {paused: true, ended: false, duration: 300, currentTime: 0});
    replacement.load = () => {};
    replacement.pause = () => {};
    audio.cloneNode = () => replacement;
    audio.replaceWith = () => {};
    Object.assign(player.oscilloscope, {source: {}, canAnalyse: () => false, releaseAudio() {}});

    player.src = "https://audio.example/track.mp3";
    assert.equal(player.audioEl, replacement);
    audio.dispatchEvent(new Event("ended"));
    assert.equal(nextCalls(), 0, "the old audio must no longer advance the queue");
    player.bindAudioEvents();
    replacement.dispatchEvent(new Event("ended"));
    assert.equal(nextCalls(), 1, "rebinding must not duplicate listeners");

    let progressStarts = 0;
    player.startProgressAnimation = () => { progressStarts++; };
    window.dispatchEvent(new Event("online"));
    assert.equal(progressStarts, 1);

    player.unbindAudioEvents();
    replacement.dispatchEvent(new Event("ended"));
    player.nextButtonEl.dispatchEvent(new Event("click"));
    window.dispatchEvent(new Event("online"));
    window.dispatchEvent(new Event("pageshow"));
    document.dispatchEvent(new Event("visibilitychange"));
    assert.equal(nextCalls(), 1);
    assert.equal(progressStarts, 1, "environment listeners must also be removed");

    player.bindAudioEvents();
    replacement.dispatchEvent(new Event("ended"));
    assert.equal(nextCalls(), 2, "listeners must work after reconnecting");
});

test("playback fragments preserve the element, replay the same stopped station, and clear artwork", () => {
    const compilers = new Map();
    let submissions = 0;
    const up = {link: {config: {followSelectors: [], preloadSelectors: []}}, form: {config: {submitSelectors: []}}, history: {config: {}}, fragment: {config: {}}, on() {}, submit() {submissions++;}, compiler: (name, fn) => compilers.set(name, fn)};
    const player = {src: "", kind: "track", plays: 0, pauses: 0, get isLive() {return this.kind === "live";}, setAttribute(name, value) { if (name === "data-kind") this.kind = value; }, getAttribute() { return this.kind; }, play() { this.plays++; }, pause() { this.pauses++; }};
    const nextTrack = {value: ""};
    const listItem = {value: ""};
    const artwork = {style: {backgroundImage: "old artwork"}};
    const actions = new Map();
    const sandbox = {up, document: {querySelector: selector => selector === "html" ? {getAttribute: () => "light"} : artwork, getElementById: id => ({player, queue_next_track: nextTrack, queue_list_item: listItem})[id] || null, dispatchEvent() {}}, navigator: {mediaSession: {setActionHandler: (name, handler) => actions.set(name, handler)}}, MediaMetadata: class {constructor(data) {Object.assign(this, data);}}, CustomEvent: class {}, console};
    vm.runInNewContext(appSource, sandbox);
    const compile = compilers.get("#now_playing");
    const fragment = {dataset: {kind: "live", url: "/radio/1/audio/", title: "Station", artist: "France", autoplay: "true"}};
    compile(fragment);
    compile(fragment);
    assert.equal(player.plays, 2);
    assert.equal(player.pauses, 1);
    assert.equal(actions.get("nexttrack"), null);
    assert.equal(artwork.style.backgroundImage, "");
    assert.equal(sandbox.navigator.mediaSession.metadata.artwork.length, 0);
    sandbox.playNext();
    assert.equal(submissions, 0, "Next must not advance radio into the queue");
    sandbox.playNext(42);
    assert.equal(submissions, 1, "an explicit library Play now must leave radio");
    assert.equal(nextTrack.value, 42);
    compile({dataset: {}});
    assert.equal(player.src, "");
    assert.equal(sandbox.navigator.mediaSession.metadata, null);
});
