// Keep the light DOM so Unpoly forms, Bootstrap menus, and queue drag handles
// continue to use the same event delegation as compact track listings.
class TrackCard extends HTMLElement {
    connectedCallback() {
        this.lifecycle?.abort();
        this.lifecycle = new AbortController();
        this.menuRequests = new Set();
        this.menuRefresh = null;
        const {signal} = this.lifecycle;
        this.addEventListener("click", event => {
            const button = event.target.closest('[data-track-action="listen"]');
            if (!button) return;
            if (document.getElementById("page_context")?.value === "queue") {
                playQueueItem(button);
            } else {
                playNext(Number(this.dataset.trackid));
            }
        }, {signal});
        this.addEventListener("show.bs.dropdown", () => this.loadMenu(), {signal});
        this.addEventListener("pointerover", event => {
            this.measureText();
            const button = event.target.closest(".track-card-menu");
            if (button && !button.contains(event.relatedTarget)) this.prefetchMenu();
        }, {signal});
        this.addEventListener("focusin", event => {
            this.measureText();
            if (event.target.closest(".track-card-menu")) this.prefetchMenu();
        }, {signal});
        this.frame = requestAnimationFrame(() => {
            this.resizeObserver = new ResizeObserver(() => this.measureText());
            this.querySelectorAll(".track-card-line").forEach(line => this.resizeObserver.observe(line));
            this.measureText();
        });
    }

    disconnectedCallback() {
        cancelAnimationFrame(this.frame);
        this.resizeObserver?.disconnect();
        this.lifecycle?.abort();
        this.menuRequests?.forEach(request => request.abort());
    }

    measureText() {
        this.querySelectorAll(".track-card-line").forEach(line => {
            const distance = Math.max(0, line.firstElementChild.scrollWidth - line.clientWidth);
            line.toggleAttribute("data-overflow", distance > 1);
            line.style.setProperty("--marquee-distance", `${-distance}px`);
            line.style.setProperty("--marquee-duration", `${Math.max(3, distance / 35 + 2)}s`);
        });
    }

    menuStatus(text) {
        const item = document.createElement("li");
        const message = document.createElement("span");
        message.className = "dropdown-item-text";
        message.setAttribute("role", "status");
        message.textContent = text;
        item.append(message);
        this.querySelector(".dropdown-menu").replaceChildren(item);
    }

    async requestMenu(background) {
        const {signal} = this.lifecycle;
        const params = new URLSearchParams({
            q: this.dataset.trackid,
            tag: this.dataset.removeFromTag || "",
            context: document.getElementById("page_context")?.value || "",
            grid: "1"
        });
        const url = `${this.dataset.menuUrl}?${params}`;
        const load = async () => {
            const request = up.request(url, {cache: true, background, origin: this.querySelector(".track-card-menu")});
            this.menuRequests.add(request);
            try {
                return await request;
            } finally {
                this.menuRequests.delete(request);
            }
        };
        let response = await load();
        // Raw up.request() calls can return expired cache entries. Refresh them
        // after mutations or cache aging so favorite/tag actions stay current.
        if (response.expired && !signal.aborted) {
            if (!this.menuRefresh || this.menuRefresh.url !== url) {
                up.cache.evict(url);
                this.menuRefresh = {url, promise: load()};
            }
            const refresh = this.menuRefresh;
            try {
                response = await refresh.promise;
            } finally {
                if (this.menuRefresh === refresh) this.menuRefresh = null;
            }
        }
        return response;
    }

    prefetchMenu() {
        // Hovering must not open the menu or show errors. Opening retries a
        // failed preload, and Unpoly shares any request that is still in flight.
        this.requestMenu(true).catch(() => {});
    }

    async loadMenu() {
        const {signal} = this.lifecycle;
        const version = this.menuVersion = (this.menuVersion || 0) + 1;
        const menu = this.querySelector(".dropdown-menu");
        this.menuStatus(this.dataset.menuLoading);
        try {
            const response = await this.requestMenu(false);
            if (signal.aborted || !this.isConnected || version !== this.menuVersion) return;
            menu.innerHTML = response.text;
            up.hello(menu);
        } catch (error) {
            if (signal.aborted || !this.isConnected || version !== this.menuVersion) return;
            this.menuStatus(this.dataset.menuError);
        }
        bootstrap.Dropdown.getInstance(this.querySelector(".track-card-menu"))?.update();
    }
}

customElements.define("track-card", TrackCard);
