// Unpoly conf.
up.link.config.followSelectors.push('a[href]');
up.link.config.preloadSelectors.push('a[href]');
up.form.config.submitSelectors.push(['form']);
up.history.config.restoreTargets = ["main"];
up.fragment.config.runScripts = false;

up.on('up:link:follow', (event, link) => {
    Array.from(document.querySelectorAll('button.show')).forEach(function (el) {
        el.classList.remove('show');
    });
    Array.from(document.querySelectorAll('.dropdown-menu')).forEach(function (el) {
        el.classList.remove('show');
    });
});


// bootstrap: Use system light/dark mode
;(function () {
    const htmlElement = document.querySelector("html");
    if (htmlElement.getAttribute("data-bs-theme") === 'auto') {
        function updateTheme() {
            document.querySelector("html").setAttribute("data-bs-theme",
                window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
        }

        window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', updateTheme)
        updateTheme();
    }
})();

function getPlayer() {
    return document.getElementById("player");
}

// Preferences arrive as main fragments while the audio player is kept alive.
up.compiler('[data-player-oscilloscope]', function (element) {
    getPlayer()?.setAttribute("data-oscilloscope", element.dataset.playerOscilloscope);
});

function togglePlayer() {
    let player = getPlayer();
    if (player.getAttribute("controls")) {
        player.removeAttribute("controls");
    } else {
        player.setAttribute("controls", "controls");
    }
}

function unblinkPlayer() {
    let blinky_playing_icon = document.getElementById("blinky_icon");
    if (blinky_playing_icon) {
        blinky_playing_icon.classList.remove("blink");
    }
}

function playQueueItem(element) {
    getPlayer().pause();
    unblinkPlayer();
    document.getElementById("queue_next_track").value = "";
    document.getElementById("queue_list_item").value = element.closest(".list-group-item").dataset.itempk;
    up.submit("#now_playing_form");
}


function removeFromQueue(element) {
    let item = element.closest(".list-group-item");
    document.getElementById("queue_remove_item").value = item.dataset.itempk;
    up.submit("#queue_remove_form");
    item.remove()
    let counter = document.getElementById("track_count");
    counter.innerText = String(parseInt(counter.innerText) - 1);
}


function playNext(insertTrack) {
    getPlayer().pause();
    unblinkPlayer();
    document.getElementById("queue_list_item").value = "";
    if (insertTrack !== undefined) {
        document.getElementById("queue_next_track").value = insertTrack;
    } else {
        document.getElementById("queue_next_track").value = "";
    }
    up.submit('#now_playing_form');
    //getPlayer().play();
}


// when track is removed from favorites, also remove from page if page is favorites
up.compiler('.music_file_removed_from_favorite', function (element) {
    if (document.getElementById("page_context")) {
        if (document.getElementById("page_context").value === "favorites") {
            let trackid = element.dataset.objectpk;
            document.getElementById("music_files_row_" + trackid).remove();
            let counter = document.getElementById("track_count");
            counter.innerText = String(parseInt(counter.innerText) - 1);
        }
    }
});

// when track is removed from page tag, also remove from page if page is tag
up.compiler('.music_file_removed_from_tag', function (element) {
    if (document.getElementById("page_context")) {
        if (document.getElementById("page_context").value === "tag") {
            let trackid = element.dataset.objectpk;
            document.getElementById("music_files_row_" + trackid).remove();
            let counter = document.getElementById("track_count");
            counter.innerText = String(parseInt(counter.innerText) - 1);
        }
    }
});

// when new track loaded in now_playing, start player
up.compiler("#now_playing", function (element) {
    if (element.dataset.url) {
        let player = getPlayer();
        if (element.dataset.url !== player.src) {
            player.src = element.dataset.url;
            if (element.dataset.autoplay === "true") {
                try {
                    player.play();
                } catch (err) {
                    /* can't start audio without user click, just ignore */
                }
            }
        }

        if ("mediaSession" in navigator) {
            navigator.mediaSession.setActionHandler('nexttrack', () => {
                playNext()
            });
            //document.querySelector('#now_playing').style.backgroundImage="url("+element.dataset.artwork512+")"; // specify the image path here
            document.querySelector('#artworkbig').style.backgroundImage = "url(" + element.dataset.artwork512 + ")"; // specify the image path here
            navigator.mediaSession.metadata = new MediaMetadata({
                title: element.dataset.title,
                artist: element.dataset.artist,
                album: element.dataset.album,
                artwork: [
                    {
                        src: element.dataset.artwork96,
                        sizes: "96x96",
                        type: "image/jpeg",
                    },
                    {
                        src: element.dataset.artwork128,
                        sizes: "128x128",
                        type: "image/jpeg",
                    },
                    {
                        src: element.dataset.artwork192,
                        sizes: "192x192",
                        type: "image/jpeg",
                    },
                    {
                        src: element.dataset.artwork256,
                        sizes: "256x256",
                        type: "image/jpeg",
                    },
                    {
                        src: element.dataset.artwork384,
                        sizes: "384x384",
                        type: "image/jpeg",
                    },
                    {
                        src: element.dataset.artwork512,
                        sizes: "512x512",
                        type: "image/jpeg",
                    },
                ],

            });
        } else {
            console.log("MediaSession not supported");
        }


    }
    const pageContext = document.getElementById("page_context");
    if (pageContext?.value === "queue") {
        up.render({
            url: pageContext.dataset.queueUrl,
            target: "main",
            history: false,
            scroll: false
        });
    }
});

up.compiler(".makeMeSortable", function (element) {
    let sortable = Sortable.create(element, {
        "onUpdate": function () {
            let positions = [];
            let list_items = Array.from(document.querySelectorAll(".queue_item"));
            for (let i = 0; i < list_items.length; i++) {
                positions.push({rank: i + 1, item: parseInt(list_items[i].dataset.itempk)})
            }
            document.getElementById("queue_new_order").value = JSON.stringify(positions);
            document.getElementById("queue_list_pk").value = element.dataset.list_pk;
            up.submit(document.getElementById("queue_order_form"));
        },
        "handle": ".sortable_handle"
    });
});


up.compiler(".track_menu", function (element) {
    // reload dropdown menu every time dropdown button is pressed
    element.addEventListener('mousedown', () => {
        let page_context = ""
        if (document.getElementById("page_context")) {
            page_context = document.getElementById("page_context").value;
        }
        let trackid = element.dataset.trackid;
        let remove_from_tag = element.dataset.removeFromTag || "";
        let playing = element.dataset.playing || "";
        let target_elements = Array.prototype.slice.call(document.getElementsByClassName("dropdown-menu-" + trackid));
        let params = new URLSearchParams({q: trackid, tag: remove_from_tag, playing: playing, context: page_context});
        fetch(dropdown_url + "?" + params.toString(), {method: "GET"})
            .then(response => response)
            .then(response => response.text())
            .then(function (body) {
                for (let i = 0; i < target_elements.length; i++) {
                    target_elements[i].innerHTML = body;
                }
                if (document.getElementById("page_context")) {
                    if (document.getElementById("page_context").value === "favorites") {
                        let add_form = document.getElementById("form_add_music_file_" + trackid + "_to_favorite");
                        if (add_form) {
                            document.getElementById("music_files_row_" + trackid).remove();
                        }
                    }
                }
            });
    });
});

up.compiler("#track-artwork-search", function (element) {
    const form = element.closest("form");
    const searchInputs = ["artist", "title", "album"].map(name => form.elements[name]);
    const updateArtworkSearch = () => {
        const query = searchInputs
            .map(input => input.value.trim())
            .filter(Boolean)
            .join(" ");
        const params = new URLSearchParams({tbm: "isch", q: query});
        element.href = "https://www.google.com/search?" + params.toString();
    };
    searchInputs.forEach(input => input.addEventListener("input", updateArtworkSearch));
    updateArtworkSearch();
});

up.compiler("#ytrip-form", function (form) {
    const urlInput = form.elements.url;
    const status = form.querySelector("[data-lookup-status]");
    const fields = ["artist", "title", "album", "year"];
    const edited = new Set();
    const autofilled = new Map();
    let timer;
    let controller;
    let requestNumber = 0;

    for (const name of fields) {
        form.elements[name].addEventListener("input", () => {
            edited.add(name);
            autofilled.delete(name);
        });
    }

    urlInput.addEventListener("input", () => {
        clearTimeout(timer);
        controller?.abort();
        requestNumber += 1;
        status.textContent = "";
        for (const [name, value] of autofilled) {
            if (form.elements[name].value === value) {
                form.elements[name].value = "";
            }
        }
        autofilled.clear();

        const url = urlInput.value.trim();
        if (!url || !urlInput.checkValidity()) {
            return;
        }
        const currentRequest = requestNumber;
        timer = setTimeout(async () => {
            controller = new AbortController();
            status.textContent = form.dataset.lookupLoading;
            try {
                const params = new URLSearchParams({url});
                const response = await fetch(form.dataset.metadataUrl + "?" + params, {
                    signal: controller.signal,
                });
                if (currentRequest !== requestNumber || !form.isConnected) {
                    return;
                }
                if (response.status === 400) {
                    status.textContent = "";
                    return;
                }
                if (!response.ok) {
                    throw new Error("Metadata lookup failed");
                }
                const metadata = await response.json();
                if (currentRequest !== requestNumber || !form.isConnected) {
                    return;
                }
                for (const name of fields) {
                    const value = metadata[name];
                    if (value && !edited.has(name) && !form.elements[name].value.trim()) {
                        form.elements[name].value = value;
                        autofilled.set(name, value);
                    }
                }
                status.textContent = "";
            } catch (error) {
                if (currentRequest === requestNumber && error.name !== "AbortError") {
                    status.textContent = form.dataset.lookupFailed;
                }
            }
        }, 500);
    });

    return () => {
        clearTimeout(timer);
        controller?.abort();
    };
});

up.compiler("body", function (element) {
    element.addEventListener("keypress", function (evt) {
        if (!evt.target.classList.contains("form-control")) {
            let playa = getPlayer();
            if (evt.key === "n") {
                evt.preventDefault();
                playNext()
            }
            /*
            if (evt.key === "p") {
                // TODO: change player icon, stop blinking
                evt.preventDefault();
                unblinkPlayer();
                playa.pause();
            }
            if (evt.key === "s") {
                // TODO: change player icon, stop blinking
                evt.preventDefault();
                unblinkPlayer();
                playa.pause();
                playa.fastSeek(0);
            }
            */
        }
    });
});
