import assert from "node:assert/strict";
import {readFile} from "node:fs/promises";
import test from "node:test";

const source = await readFile(new URL("../internal/assets/static/butterchurn-visualization.js", import.meta.url), "utf8");
const {ButterchurnVisualization} = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`);

function fixture(context) {
    context.mock.method(Math, "random", () => 0);
    const previousDocument = globalThis.document;
    const canvases = [];
    globalThis.document = {createElement() {
        const canvas = new EventTarget();
        canvas.width = 300;
        canvas.height = 150;
        canvas.lost = false;
        canvas.getContext = () => ({getExtension: () => ({loseContext: () => { canvas.lost = true; }})});
        canvases.push(canvas);
        return canvas;
    }};
    context.after(() => { globalThis.document = previousDocument; });
    const connections = [];
    const disconnections = [];
    const instances = [];
    const engine = {createVisualizer(audioContext, canvas, options) {
        const instance = {
            audioContext, canvas, options, frames: 0, presets: [],
            connectAudio: source => connections.push(source),
            disconnectAudio: source => disconnections.push(source),
            loadPreset(preset, blend) { this.presets.push([preset, blend]); },
            setRendererSize(width, height) { this.options = {...this.options, width, height}; },
            render() { this.frames += 1; },
        };
        instances.push(instance);
        return instance;
    }};
    const presets = [{preset: {name: "one"}}, {preset: {name: "two"}}, {preset: {name: "three"}}];
    const failures = [];
    const visualization = new ButterchurnVisualization(engine, presets, () => failures.push(true));
    const output = {width: 3840, height: 2160, getContext: () => ({drawImage() {}})};
    return {visualization, output, canvases, instances, connections, disconnections, failures};
}

test("Butterchurn reuses the audio source, caps resolution, and releases only its own branch", context => {
    const {visualization, output, canvases, instances, connections, disconnections, failures} = fixture(context);
    const audioContext = {};
    const audioSource = {};
    visualization.draw(audioContext, audioSource, output);
    visualization.draw(audioContext, audioSource, output);
    assert.deepEqual(connections, [audioSource]);
    assert.equal(instances.length, 1);
    assert.equal(instances[0].frames, 2);
    assert.equal(instances[0].options.width, 1280);
    assert.equal(instances[0].options.height, 720);
    visualization.nextPreset();
    assert.deepEqual(instances[0].presets.at(-1), [{name: "two"}, 2]);
    output.width = 780;
    output.height = 1688;
    visualization.draw(audioContext, audioSource, output);
    assert.equal(instances[0].options.height, 720);
    assert.ok(instances[0].options.width < 780);
    visualization.dispose();
    assert.deepEqual(disconnections, [audioSource]);
    assert.equal(canvases[0].lost, true);
    assert.deepEqual(failures, []);
});

test("a replacement audio context rebuilds the effect after delegated playback", context => {
    const {visualization, output, canvases, instances, connections, disconnections} = fixture(context);
    const first = {};
    const second = {};
    visualization.draw({}, first, output);
    visualization.draw({}, second, output);
    assert.equal(instances.length, 2);
    assert.equal(canvases[0].lost, true);
    assert.deepEqual(connections, [first, second]);
    assert.deepEqual(disconnections, [first]);
    visualization.dispose();
});

test("WebGL context loss requests a fallback", context => {
    const {visualization, canvases, failures} = fixture(context);
    canvases[0].dispatchEvent(new Event("webglcontextlost", {cancelable: true}));
    assert.equal(failures.length, 1);
    visualization.dispose();
});

test("random selection reaches either other preset without repeating the current effect", context => {
    const {visualization, output, instances} = fixture(context);
    visualization.draw({}, {}, output);
    for (let current = 0; current < visualization.presets.length; current += 1) {
        const selected = new Set();
        for (const random of [0, 0.999999]) {
            visualization.presetIndex = current;
            context.mock.method(Math, "random", () => random);
            visualization.randomPreset();
            assert.notEqual(visualization.presetIndex, current);
            selected.add(visualization.presetIndex);
            assert.deepEqual(instances[0].presets.at(-1), [visualization.presets[visualization.presetIndex].preset, 2]);
        }
        assert.equal(selected.size, 2);
    }
    visualization.dispose();
});

test("the initial random selection survives a track change before the first audio frame", context => {
    const {visualization, output, instances} = fixture(context);
    context.mock.method(Math, "random", () => 0.999999);
    const pending = new ButterchurnVisualization(visualization.engine, visualization.presets, () => {});
    assert.equal(pending.presetIndex, 2);
    pending.randomPreset();
    const chosen = pending.presets[pending.presetIndex].preset;
    pending.draw({}, {}, output);
    assert.deepEqual(instances[0].presets, [[chosen, 0]]);
    pending.dispose();
    visualization.dispose();
});
