export class FooterOscilloscope {
    constructor(player) {
        this.player = player;
        this.enabled = player.getAttribute?.("data-oscilloscope") !== "false";
        this.audioContext = null;
        this.source = null;
        this.analyser = null;
        this.samples = new Uint8Array(2048);
        this.canvas = null;
        this.overlay = null;
        this.overlayRenderer = null;
        this.frame = null;
        this.lastDraw = 0;
        this.preparing = null;
        this.motionPreference = window.matchMedia("(prefers-reduced-motion: reduce)");
        this.onEnvironmentChange = () => {
            this.stop();
            if (document.visibilityState !== "hidden" && this.playbackActive) {
                this.prepare();
            }
        };
        this.onGesture = () => {
            if (this.playbackActive) {
                this.prepare();
            }
        };
        this.onResize = () => {
            this.resize();
            this.resizeCanvas(this.overlay);
        };
    }

    get visualizing() {
        return Boolean(this.overlay || this.enabled) && !this.reducedMotion;
    }

    get playbackActive() {
        const audio = this.player.audioEl;
        return this.player.playbackRequested || Boolean(audio && !audio.paused && !audio.ended);
    }

    setOverlay(canvas, renderer = null) {
        this.stop();
        this.overlay = canvas;
        this.overlayRenderer = renderer;
        this.resizeCanvas(canvas);
        if (this.playbackActive) {
            this.prepare();
        }
    }

    setEnabled(enabled) {
        this.enabled = enabled;
        this.stop();
        const canvas = this.player.closest("footer")?.querySelector(".footer-oscilloscope");
        if (canvas) {
            canvas.hidden = !enabled;
        }
        if (this.visualizing && this.playbackActive) {
            this.prepare();
        }
    }

    canAnalyse(src) {
        return Boolean(src) && new URL(src, window.location.href).origin === window.location.origin;
    }

    get reducedMotion() {
        // Windows disabling system animations must not override the player's
        // explicit waveform preference. Respect reduced motion without one.
        return this.motionPreference.matches && this.player.getAttribute?.("data-oscilloscope") !== "true";
    }

    connect() {
        this.setEnabled(this.enabled);
        document.addEventListener("visibilitychange", this.onEnvironmentChange);
        document.addEventListener("pointerdown", this.onGesture, {passive: true});
        document.addEventListener("keydown", this.onGesture);
        window.addEventListener("pageshow", this.onEnvironmentChange);
        this.motionPreference.addEventListener("change", this.onEnvironmentChange);
        this.start();
    }

    disconnect() {
        this.stop();
        this.resizeObserver?.disconnect();
        document.removeEventListener("visibilitychange", this.onEnvironmentChange);
        document.removeEventListener("pointerdown", this.onGesture);
        document.removeEventListener("keydown", this.onGesture);
        window.removeEventListener("pageshow", this.onEnvironmentChange);
        this.motionPreference.removeEventListener("change", this.onEnvironmentChange);
    }

    prepare() {
        const AudioContextClass = window.AudioContext || window.webkitAudioContext;
        if (!AudioContextClass || (!this.visualizing && !this.source) || !this.canAnalyse(this.player.src)) {
            return;
        }
        if (this.preparing) {
            return;
        }
        try {
            this.audioContext ||= new AudioContextClass();
            const context = this.audioContext;
            // Resume inside the play/gesture handler, but only reroute native audio
            // once it succeeds. A blocked context must not silence playback.
            this.preparing = context.resume().then(() => {
                if (context !== this.audioContext || context.state !== "running" || !this.player.isConnected
                    || (!this.visualizing && !this.source) || !this.canAnalyse(this.player.src)) {
                    return;
                }
                if (!this.source) {
                    this.analyser = context.createAnalyser();
                    this.analyser.fftSize = this.samples.length;
                    this.source = context.createMediaElementSource(this.player.audioEl);
                    // The analyser is a side branch; the audible signal stays unmodified.
                    this.source.connect(context.destination);
                    this.source.connect(this.analyser);
                    context.addEventListener("statechange", this.onEnvironmentChange);
                }
                this.start();
            }).catch(() => {
                this.stop();
            }).finally(() => {
                this.preparing = null;
            });
        } catch {
            this.stop();
        }
    }

    releaseAudio() {
        this.stop();
        const context = this.audioContext;
        this.audioContext = null;
        this.source = null;
        this.analyser = null;
        this.preparing = null;
        if (context) {
            context.removeEventListener("statechange", this.onEnvironmentChange);
            context.close().catch(() => {});
        }
    }

    resize() {
        this.resizeCanvas(this.canvas);
    }

    resizeCanvas(canvas) {
        if (!canvas) {
            return;
        }
        const bounds = canvas.getBoundingClientRect();
        const scale = Math.min(window.devicePixelRatio || 1, 2);
        canvas.width = Math.round(bounds.width * scale);
        canvas.height = Math.round(bounds.height * scale);
    }

    start() {
        if (!this.visualizing || this.frame !== null || !this.analyser || this.audioContext.state !== "running"
            || this.player.audioEl.paused || this.player.audioEl.ended || !this.player.isConnected
            || document.visibilityState === "hidden") {
            return;
        }
        const canvas = this.player.closest("footer")?.querySelector(".footer-oscilloscope");
        if (!canvas || !canvas.getContext("2d")) {
            return;
        }
        this.resizeObserver?.disconnect();
        this.canvas = canvas;
        this.resize();
        this.resizeCanvas(this.overlay);
        this.resizeObserver = new ResizeObserver(this.onResize);
        this.resizeObserver.observe(canvas);
        if (this.overlay) {
            this.resizeObserver.observe(this.overlay);
        }
        const draw = (time) => {
            if (!this.visualizing || document.visibilityState === "hidden" || this.player.audioEl.paused
                || this.player.audioEl.ended || this.audioContext?.state !== "running") {
                this.stop();
                return;
            }
            this.frame = window.requestAnimationFrame(draw);
            if (time - this.lastDraw < 1000 / 30) {
                return;
            }
            this.lastDraw = time;
            this.draw();
        };
        this.frame = window.requestAnimationFrame(draw);
    }

    stop() {
        this.resizeObserver?.disconnect();
        if (this.frame !== null) {
            window.cancelAnimationFrame(this.frame);
            this.frame = null;
        }
        const context = this.canvas?.getContext("2d");
        context?.clearRect(0, 0, this.canvas.width, this.canvas.height);
        const overlayContext = this.overlay?.getContext("2d");
        overlayContext?.clearRect(0, 0, this.overlay.width, this.overlay.height);
    }

    draw() {
        const context = this.canvas.getContext("2d");
        const {width, height} = this.canvas;
        this.analyser.getByteTimeDomainData(this.samples);
        context.clearRect(0, 0, width, height);
        if (this.overlay && !this.reducedMotion) {
            this.drawOverlay();
        }
        if (!this.enabled || this.reducedMotion || this.overlay) {
            return;
        }
        // Trigger on a rising zero crossing to keep periodic signals steady.
        let start = 0;
        for (let i = 1; i < 512; i += 1) {
            if (this.samples[i - 1] < 128 && this.samples[i] >= 128) {
                start = i;
                break;
            }
        }
        context.beginPath();
        for (let i = 0; i < 512; i += 1) {
            const x = i * width / 511;
            const y = height / 2 + ((this.samples[start + i * 2] - 128) / 128) * height * 0.38;
            if (i === 0) {
                context.moveTo(x, y);
            } else {
                context.lineTo(x, y);
            }
        }
        context.strokeStyle = window.getComputedStyle(this.canvas).color;
        context.lineJoin = "round";
        context.lineWidth = 7;
        context.globalAlpha = 0.12;
        context.stroke();
        context.lineWidth = 1.5 * Math.min(window.devicePixelRatio || 1, 2);
        context.globalAlpha = 0.8;
        context.stroke();
    }

    drawOverlay() {
        const context = this.overlay.getContext("2d");
        if (!context) {
            return;
        }
        if (this.overlayRenderer) {
            this.overlayRenderer.draw(this.audioContext, this.source, this.overlay);
            return;
        }
        const {width, height} = this.overlay;
        // A translucent wash leaves a short phosphor trail from the real signal.
        context.fillStyle = "rgba(5, 5, 5, 0.22)";
        context.fillRect(0, 0, width, height);
        let start = 0;
        for (let index = 1; index < 512; index += 1) {
            if (this.samples[index - 1] < 128 && this.samples[index] >= 128) {
                start = index;
                break;
            }
        }
        context.beginPath();
        for (let index = 0; index < 768; index += 1) {
            const x = index * width / 767;
            const y = height * 0.52 + (this.samples[start + index * 2] - 128) / 128 * height * 0.43;
            if (index === 0) {
                context.moveTo(x, y);
            } else {
                context.lineTo(x, y);
            }
        }
        context.strokeStyle = "#c8ff3d";
        context.lineJoin = "round";
        context.lineWidth = 10;
        context.globalAlpha = 0.07;
        context.stroke();
        context.lineWidth = 2 * Math.min(window.devicePixelRatio || 1, 2);
        context.globalAlpha = 0.65;
        context.stroke();
        context.globalAlpha = 1;
    }
}
