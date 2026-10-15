import assert from "node:assert/strict";
import {readFile} from "node:fs/promises";
import test from "node:test";

const source = await readFile(new URL("../internal/assets/static/fullscreen-player.js", import.meta.url), "utf8");
const {FullscreenPlayer} = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`);

class Element extends EventTarget {
    dataset = {};
    hidden = false;
    value = "waveform";
    textContent = "";
    open = false;
    showModal() { this.open = true; }
    close() { this.open = false; this.dispatchEvent(new Event("close")); }
}

function fixture(context) {
    const previousDocument = globalThis.document;
    const elements = Object.fromEntries([
        "canvas", "#fullscreen-visualization", ".fullscreen-player__effect", ".fullscreen-player__status",
        ".fullscreen-player__next", ".fullscreen-player__close",
    ].map(selector => [selector, new Element()]));
    elements[".fullscreen-player__status"].dataset = {loading: "loading", unavailable: "unavailable"};
    const dialog = new Element();
    dialog.querySelector = selector => elements[selector];
    const document = new Element();
    document.getElementById = id => id === "fullscreen-player" ? dialog : null;
    document.body = {classList: {add() {}, remove() {}}};
    globalThis.document = document;
    context.after(() => { globalThis.document = previousDocument; });
    const overlays = [];
    const player = {oscilloscope: {setOverlay: (...args) => overlays.push(args)}};
    const fullscreen = new FullscreenPlayer(player);
    context.mock.method(fullscreen, "updateTrack", () => {});
    fullscreen.connect();
    return {fullscreen, dialog, overlays};
}

function effect() {
    return {disposed: false, dispose() { this.disposed = true; }};
}

test("the waveform is the default and does not load Butterchurn", context => {
    const {fullscreen, dialog, overlays} = fixture(context);
    const loader = context.mock.method(fullscreen, "loadVisualization");
    fullscreen.toggle();
    assert.equal(fullscreen.visualizationSelect.value, "waveform");
    assert.equal(loader.mock.callCount(), 0);
    assert.deepEqual(overlays.at(-1), [dialog.querySelector("canvas")]);
});

test("switching back or closing releases the effect and restores the waveform", async context => {
    const {fullscreen, dialog, overlays} = fixture(context);
    const effects = [];
    context.mock.method(fullscreen, "loadVisualization", async () => {
        const instance = effect();
        effects.push(instance);
        return instance;
    });
    fullscreen.toggle();
    fullscreen.visualizationSelect.value = "butterchurn";
    await fullscreen.updateVisualization();
    assert.equal(fullscreen.effectButton.hidden, false);
    assert.equal(overlays.at(-1)[1], effects[0]);
    fullscreen.visualizationSelect.value = "waveform";
    await fullscreen.updateVisualization();
    assert.equal(effects[0].disposed, true);
    assert.equal(fullscreen.effectButton.hidden, true);
    fullscreen.visualizationSelect.value = "butterchurn";
    await fullscreen.updateVisualization();
    dialog.close();
    assert.equal(effects[1].disposed, true);
    assert.deepEqual(overlays.at(-1), [null]);
    assert.equal(fullscreen.visualizationSelect.value, "butterchurn");
});

test("a late download cannot reopen an effect after closing or changing selection", async context => {
    for (const close of [true, false]) {
        const {fullscreen, dialog, overlays} = fixture(context);
        let resolve;
        context.mock.method(fullscreen, "loadVisualization", () => new Promise(done => { resolve = done; }));
        fullscreen.toggle();
        fullscreen.visualizationSelect.value = "butterchurn";
        const pending = fullscreen.updateVisualization();
        if (close) {
            dialog.close();
        } else {
            fullscreen.visualizationSelect.value = "waveform";
            await fullscreen.updateVisualization();
        }
        const instance = effect();
        resolve(instance);
        await pending;
        assert.equal(instance.disposed, true);
        assert.equal(fullscreen.visualization, null);
        assert.equal(overlays.at(-1).length, 1);
    }
});

test("load and render failures fall back without interrupting playback", async context => {
    const {fullscreen, overlays} = fixture(context);
    fullscreen.toggle();
    context.mock.method(fullscreen, "loadVisualization", async () => { throw new Error("No WebGL"); });
    fullscreen.visualizationSelect.value = "butterchurn";
    await fullscreen.updateVisualization();
    assert.equal(fullscreen.visualizationSelect.value, "waveform");
    assert.equal(fullscreen.visualizationStatus.textContent, "unavailable");
    assert.equal(overlays.at(-1).length, 1);

    let fail;
    const instance = effect();
    context.mock.method(fullscreen, "loadVisualization", async onError => { fail = onError; return instance; });
    fullscreen.visualizationSelect.value = "butterchurn";
    await fullscreen.updateVisualization();
    fail();
    assert.equal(instance.disposed, true);
    assert.equal(fullscreen.visualizationSelect.value, "waveform");
    assert.equal(fullscreen.visualizationStatus.textContent, "unavailable");
});

test("track changes randomize Butterchurn while repeated metadata updates keep the current effect", async context => {
    const {fullscreen, dialog} = fixture(context);
    const instance = effect();
    let selections = 0;
    instance.randomPreset = () => { selections += 1; };
    context.mock.method(fullscreen, "loadVisualization", async () => instance);
    fullscreen.toggle();
    fullscreen.visualizationSelect.value = "butterchurn";
    await fullscreen.updateVisualization();
    const notify = track => {
        const event = new Event("reinhardt:track");
        event.detail = track;
        document.dispatchEvent(event);
    };
    notify({url: "/stream/1/", position: "1"});
    assert.equal(selections, 1);
    notify({url: "/stream/1/", position: "1", title: "Updated title"});
    assert.equal(selections, 1);
    notify({url: "/stream/2/", position: "2"});
    assert.equal(selections, 2);
    notify({url: "/stream/2/", position: "3"});
    assert.equal(selections, 3);
    notify({});
    assert.equal(selections, 3);
    dialog.close();
    notify({url: "/stream/3/", position: "4"});
    assert.equal(selections, 3);
    assert.equal(instance.disposed, true);
});
