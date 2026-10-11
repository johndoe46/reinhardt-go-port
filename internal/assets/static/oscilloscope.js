export class FooterOscilloscope {
    constructor(player) {
        this.player = player;
        this.enabled = player.getAttribute?.("data-oscilloscope") !== "false";
        this.audioContext = null;
        this.source = null;
        this.analyser = null;
        this.samples = new Uint8Array(2048);
        this.canvas = null;
        this.frame = null;
        this.lastDraw = 0;
        this.preparing = null;
        this.motionPreference = window.matchMedia("(prefers-reduced-motion: reduce)");
        this.onEnvironmentChange = () => {
            this.stop();
            if (document.visibilityState !== "hidden" && this.player.playbackRequested) {
                this.prepare();
            }
        };
        this.onGesture = () => {
            if (this.player.playbackRequested) {
                this.prepare();
            }
        };
        this.onResize = () => this.resize();
    }

    setEnabled(enabled) {
        this.enabled = enabled;
        this.stop();
        const canvas = this.player.closest("footer")?.querySelector(".footer-oscilloscope");
        if (canvas) {
            canvas.hidden = !enabled;
        }
        if (enabled && this.player.playbackRequested) {
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
        if (!AudioContextClass || ((!this.enabled || this.reducedMotion) && !this.source) || !this.canAnalyse(this.player.src)) {
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
                    || (!this.enabled && !this.source) || !this.canAnalyse(this.player.src)) {
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
        if (!this.canvas) {
            return;
        }
        const bounds = this.canvas.getBoundingClientRect();
        const scale = Math.min(window.devicePixelRatio || 1, 2);
        this.canvas.width = Math.round(bounds.width * scale);
        this.canvas.height = Math.round(bounds.height * scale);
    }

    start() {
        if (!this.enabled || this.frame !== null || !this.analyser || this.audioContext.state !== "running"
            || this.player.audioEl.paused || this.player.audioEl.ended || !this.player.isConnected
            || document.visibilityState === "hidden" || this.reducedMotion) {
            return;
        }
        const canvas = this.player.closest("footer")?.querySelector(".footer-oscilloscope");
        if (!canvas || !canvas.getContext("2d")) {
            return;
        }
        this.resizeObserver?.disconnect();
        this.canvas = canvas;
        this.resize();
        this.resizeObserver = new ResizeObserver(this.onResize);
        this.resizeObserver.observe(canvas);
        const draw = (time) => {
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
        if (this.frame !== null) {
            window.cancelAnimationFrame(this.frame);
            this.frame = null;
        }
        const context = this.canvas?.getContext("2d");
        context?.clearRect(0, 0, this.canvas.width, this.canvas.height);
    }

    draw() {
        const context = this.canvas.getContext("2d");
        const {width, height} = this.canvas;
        this.analyser.getByteTimeDomainData(this.samples);
        context.clearRect(0, 0, width, height);
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
}
