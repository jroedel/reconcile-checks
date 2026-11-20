// The page's open connection to the program (docs/03-the-local-app.md, "When
// it closes").
//
// While it is open the program knows a page is in use and stays running. It
// also tells the page what has happened: its first event is the program's
// version, so a page that reconnects to a different version -- the program
// restarted into an update -- reloads itself rather than carrying on with a
// page the new version may not understand.
//
// When the connection is lost for more than a few seconds, the page says that
// the program has closed, instead of showing a form that looks as if it would
// still work.

const lostAfter = 10_000; // ms without the program before saying it has closed
const retryEvery = 3_000; // ms between attempts once the browser has given up

const banner = document.getElementById("closed");
let version = null;
let lostSince = null;

function connect() {
	const events = new EventSource("/events");

	events.addEventListener("hello", (e) => {
		if (version !== null && e.data !== version) {
			location.reload();
			return;
		}

		version = e.data;
		lostSince = null;
		if (banner) banner.hidden = true;
	});

	events.onerror = () => {
		lostSince ??= Date.now();

		if (banner && Date.now() - lostSince > lostAfter) {
			banner.hidden = false;
		}

		// The browser retries a dropped connection by itself, but not one
		// that was refused; try again from here.
		if (events.readyState === EventSource.CLOSED) {
			events.close();
			setTimeout(connect, retryEvery);
		}
	};
}

connect();
