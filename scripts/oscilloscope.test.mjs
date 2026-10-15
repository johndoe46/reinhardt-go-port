import assert from "node:assert/strict";
import {readFile} from "node:fs/promises";
import test from "node:test";

const source = await readFile(new URL("../internal/assets/static/oscilloscope.js", import.meta.url), "utf8");
const {FooterOscilloscope} = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`);

function fixture(context, {enabled = "true", reducedMotion = false, paused = false, playbackRequested = true} = {}) {
    const previousWindow = globalThis.window;
    globalThis.window = {
        location: {href: "http://localhost/"},
        matchMedia: () => ({matches: reducedMotion}),
    };
    context.after(() => { globalThis.window = previousWindow; });
    const canvasContext = {clearRect() {}};
    const canvas = {
        getBoundingClientRect: () => ({width: 1200, height: 800}),
        getContext: () => canvasContext,
    };
    const player = {
        getAttribute: () => enabled,
        closest: () => ({querySelector: () => canvas}),
        audioEl: {paused, ended: false},
        playbackRequested,
    };
    const scope = new FooterOscilloscope(player);
    context.mock.method(scope, "stop", () => {});
    context.mock.method(scope, "prepare", () => {});
    return {scope, canvas};
}

test("the full screen waveform honors the existing explicit animation preference", context => {
    const {scope, canvas} = fixture(context, {reducedMotion: true});
    scope.setOverlay(canvas);
    assert.equal(scope.visualizing, true);
});

test("reduced motion still applies without an explicit animation preference", context => {
    const {scope, canvas} = fixture(context, {enabled: "false", reducedMotion: true});
    scope.setOverlay(canvas);
    assert.equal(scope.visualizing, false);
});

test("the full screen waveform works with the footer waveform disabled", context => {
    const {scope, canvas} = fixture(context, {enabled: "false"});
    scope.setOverlay(canvas);
    assert.equal(scope.visualizing, true);
});

test("opening the overlay prepares analysis for audio started through native media controls", context => {
    const {scope, canvas} = fixture(context, {playbackRequested: false});
    scope.setOverlay(canvas);
    assert.equal(scope.prepare.mock.callCount(), 1);
});

test("enabling the waveform prepares analysis during native playback", context => {
    const {scope} = fixture(context, {playbackRequested: false});
    scope.setEnabled(true);
    assert.equal(scope.prepare.mock.callCount(), 1);
});

test("opening an overlay without playback does not start audio analysis", context => {
    const {scope, canvas} = fixture(context, {playbackRequested: false, paused: true});
    scope.setOverlay(canvas);
    assert.equal(scope.prepare.mock.callCount(), 0);
});

test("drawing the overlay uses the same motion decision as starting it", context => {
    const {scope, canvas} = fixture(context, {reducedMotion: true});
    scope.canvas = scope.overlay = canvas;
    scope.analyser = {getByteTimeDomainData() {}};
    context.mock.method(scope, "drawOverlay", () => {});
    scope.draw();
    assert.equal(scope.drawOverlay.mock.callCount(), 1);
});

test("the alternative renderer shares analysis without changing the footer drawing path", context => {
    const {scope, canvas} = fixture(context, {enabled: "false"});
    const calls = [];
    const renderer = {draw: (...args) => calls.push(args)};
    scope.canvas = canvas;
    scope.audioContext = {state: "running"};
    scope.source = {};
    scope.analyser = {getByteTimeDomainData() {}};
    scope.setOverlay(canvas, renderer);
    scope.draw();
    assert.deepEqual(calls, [[scope.audioContext, scope.source, canvas]]);
    scope.setOverlay(canvas);
    assert.equal(scope.overlayRenderer, null);
});

test("reduced motion also suppresses the alternative visualization", context => {
    const {scope, canvas} = fixture(context, {enabled: "false", reducedMotion: true});
    let draws = 0;
    scope.canvas = canvas;
    scope.analyser = {getByteTimeDomainData() {}};
    scope.setOverlay(canvas, {draw: () => { draws += 1; }});
    scope.draw();
    assert.equal(draws, 0);
});
