# Device evidence — SM-A165F, Android 16, 2026-10-10

QA package `com.thothterm.debian.qa.nextgen` only (debug-signed, own data root).
`protected-before.txt` / `protected-after.txt`: APK hashes of the five production apps, identical.
`production-idle-readonly.txt`: read-only /proc sampling of the owner's live engine + 4 containers (no signal sent).
`qa-idle-readonly.txt`: the same for the QA engine with 2 containers.
`netip-first-try.txt`, `compose-device.txt`: --net-ip and Compose on Android. `banner-thothdock-mark.png`: the new banner.

**Incident:** at 17:23:05 Android's phantom-process trimmer killed the production
engine (`Killing PhantomProcessRecord 10918:libthothdock.so`) after the QA app's
processes raised the device-wide total over the cap. The owner's four production
containers stopped (no restart policy). Not caused by any command aimed at the
production app; caused by running a second engine. See ../../ANDROID_LIFECYCLE.md.
2026-10-10: com.thothterm.debian (0.3.0, owner-authorised) uninstalled after clean Exit; owner confirmed the data in it (incl. /home/thoth/M/Pictures copy) was test data.
