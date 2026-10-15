let resources;

export async function createButterchurn(onError) {
    resources ||= Promise.all([
        import("./vendor/butterchurn/butterchurn-2.6.7.min.js"),
        fetch(new URL("./vendor/butterchurn/presets-2.4.7.json", import.meta.url)).then(response => {
            if (!response.ok) {
                throw new Error("Unable to load Butterchurn presets");
            }
            return response.json();
        }),
    ]).catch(error => {
        resources = null;
        throw error;
    });
    const [, presets] = await resources;
    return new ButterchurnVisualization(window.butterchurn.default, presets, onError);
}

export class ButterchurnVisualization {
    constructor(engine, presets, onError) {
        this.engine = engine;
        this.presets = presets;
        this.onError = onError;
        this.presetIndex = Math.floor(Math.random() * presets.length);
        this.onContextLost = event => {
            event.preventDefault();
            this.onError();
        };
        this.createCanvas();
    }

    createCanvas() {
        // Render offscreen, then copy to the existing 2D overlay. The default
        // waveform can immediately reclaim that canvas if WebGL fails.
        this.canvas = document.createElement("canvas");
        this.gl = this.canvas.getContext("webgl2", {alpha: false, antialias: false});
        if (!this.gl) {
            throw new Error("WebGL 2 is unavailable");
        }
        this.canvas.addEventListener("webglcontextlost", this.onContextLost);
    }

    draw(audioContext, source, output) {
        try {
            if (this.audioContext && this.audioContext !== audioContext) {
                this.release();
                this.createCanvas();
            }
            // Limit GPU work independently of the screen's pixel density.
            const scale = Math.min(1, 1280 / output.width, 720 / output.height);
            const width = Math.max(1, Math.round(output.width * scale));
            const height = Math.max(1, Math.round(output.height * scale));
            const resized = this.canvas.width !== width || this.canvas.height !== height;
            if (resized) {
                this.canvas.width = width;
                this.canvas.height = height;
            }
            if (!this.visualizer) {
                this.audioContext = audioContext;
                this.visualizer = this.engine.createVisualizer(audioContext, this.canvas, {
                    width, height, pixelRatio: 1, textureRatio: 1, meshWidth: 32, meshHeight: 24,
                });
                this.visualizer.loadPreset(this.presets[this.presetIndex].preset, 0);
            } else if (resized) {
                this.visualizer.setRendererSize(width, height);
            }
            if (this.source !== source) {
                if (this.source) {
                    this.visualizer.disconnectAudio(this.source);
                }
                // A side branch only: never connect this engine to the speakers.
                this.visualizer.connectAudio(source);
                this.source = source;
            }
            const now = performance.now();
            const elapsedTime = Math.min((now - (this.lastDraw || now)) / 1000 || 1 / 30, 0.1);
            this.lastDraw = now;
            this.visualizer.render({elapsedTime});
            output.getContext("2d").drawImage(this.canvas, 0, 0, output.width, output.height);
        } catch {
            this.onError();
        }
    }

    nextPreset() {
        this.selectPreset((this.presetIndex + 1) % this.presets.length);
    }

    randomPreset() {
        if (this.presets.length > 1) {
            const offset = 1 + Math.floor(Math.random() * (this.presets.length - 1));
            this.selectPreset((this.presetIndex + offset) % this.presets.length);
        }
    }

    selectPreset(index) {
        this.presetIndex = index;
        try {
            this.visualizer?.loadPreset(this.presets[this.presetIndex].preset, 2);
        } catch {
            this.onError();
        }
    }

    release() {
        if (this.visualizer && this.source) {
            // A delegated stream may already have closed the old audio context.
            try {
                this.visualizer.disconnectAudio(this.source);
            } catch {}
        }
        this.canvas.removeEventListener("webglcontextlost", this.onContextLost);
        this.gl.getExtension("WEBGL_lose_context")?.loseContext();
        this.visualizer = null;
        this.source = null;
        this.audioContext = null;
        this.lastDraw = 0;
    }

    dispose() {
        this.release();
    }
}
