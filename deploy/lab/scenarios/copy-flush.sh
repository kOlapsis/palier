# The copy of a volume and its restore are written to disk before their helper container exits. A stack with a volume
# of SIZE_MB (2048 by default) is updated to a version that exits, then rolled back, while the dirty pages of the machine
# are sampled and the helper containers inspected in a loop. The same copy is then done by hand, without and with a final sync.
reg=10.10.50.10:5443
repo=$reg/lab/flush
size=${SIZE_MB:-2048}
echo lab-registry-password | docker login $reg -u palier --password-stdin >/dev/null 2>&1
lab_image "$repo:1.0.0" "exec sleep infinity"
lab_image "$repo:1.1.0" "echo flush 1.1.0 broken; exit 1"
for v in 1.0.0 1.1.0; do docker push -q "$repo:$v" >/dev/null; done
docker rmi "$repo:1.1.0" >/dev/null
sql "DELETE FROM tag_dates WHERE image = '$repo'"
mkdir -p /apps/flush
cat >/apps/flush/compose.yaml <<Y
services:
  data:
    image: $repo:1.0.0
    volumes:
      - big:/data
    labels:
      palier.auto: "off"
      palier.maturation: 0s
volumes:
  big: {}
Y
(cd /apps/flush && docker compose up -d >/dev/null 2>&1)
docker exec flush-data-1 sh -c "dd if=/dev/urandom of=/data/big bs=1M count=$size 2>/dev/null; sync"
sum() { docker exec flush-data-1 sha256sum /data/big | cut -c1-16; }
before=$(sum)
echo "flush: $(docker inspect flush-data-1 --format '{{.Config.Image}} {{.State.Status}}'), /data/big $size MB, sha256 $before…"
echo "machine: $(awk '/^MemTotal:/ {printf "%d MB of memory", $2 / 1024}' /proc/meminfo), vm.dirty_background_ratio $(cat /proc/sys/vm/dirty_background_ratio), vm.dirty_ratio $(cat /proc/sys/vm/dirty_ratio)"

# watched <command…>: runs the command with an empty page cache while the dirty pages of the machine are sampled and every
# helper container is inspected in a loop, then says for each helper how long it ran and how long an inspect had to wait.
watched() {
	sync
	echo 3 >/proc/sys/vm/drop_caches
	(while :; do echo "$(date +%s.%N) $(awk '/^Dirty:|^Writeback:/ {s += $2} END {print s}' /proc/meminfo)"; sleep 0.2; done) >/tmp/dirty.log &
	sampler=$!
	(while :; do
		for c in $(docker ps -q --no-trunc --filter label=palier.helper); do
			s=$(date +%s.%N)
			docker inspect -f '{{.State.Status}}' "$c" >/dev/null 2>&1 || true
			echo "$c $s $(date +%s.%N)"
		done
		sleep 0.1
	done) >/tmp/inspect.log &
	poller=$!
	docker events --filter type=container --filter label=palier.helper --filter event=start --filter event=die --format '{{.TimeNano}} {{.Action}} {{.Actor.ID}} {{index .Actor.Attributes "palier.helper"}}' >/tmp/events.log &
	events=$!
	sleep 1
	"$@"
	sleep 2
	kill $sampler $poller $events 2>/dev/null || true
	wait $sampler $poller $events 2>/dev/null || true
	python3 - <<'EOF'
samples = [(float(t), int(kb)) for t, kb in (l.split() for l in open("/tmp/dirty.log") if len(l.split()) == 2)]
inspects = {}
for l in open("/tmp/inspect.log"):
    f = l.split()
    if len(f) == 3:
        inspects.setdefault(f[0], []).append(float(f[2]) - float(f[1]))
started = {}
for l in open("/tmp/events.log"):
    f = l.split()
    if len(f) < 4:
        continue
    t, action, cid = int(f[0]) / 1e9, f[1], f[2]
    if action == "start":
        started[cid] = t
    elif action == "die" and cid in started:
        during = [kb for at, kb in samples if started[cid] <= at <= t] or [0]
        waits = inspects.get(cid) or [0]
        print("  helper %-11s ran %5.1f s, dirty pages at most %4d MB, %3d inspects, the longest answered in %.2f s" % (f[3] + ":", t - started[cid], max(during) / 1024, len(waits), max(waits)))
EOF
}

rescan flush
echo "== Palier: update to a version that exits, then rollback"
watched apply_and_report flush
echo "after: $(docker inspect flush-data-1 --format '{{.Config.Image}} {{.State.Status}}'), sha256 $(sum)… (before $before…)"

echo "== the same copy by hand, without then with a final sync, three times"
for end in true sync true sync true sync; do
	docker volume create flush-probe >/dev/null
	watched docker run --rm --label palier.helper=by-hand-$end -v flush_big:/from:ro -v flush-probe:/to busybox:1.37.0 sh -c "cp -a /from/. /to/ && $end"
	docker volume rm flush-probe >/dev/null
done

(cd /apps/flush && docker compose down -v --rmi all >/dev/null 2>&1)
rm -rf /apps/flush
docker logout $reg >/dev/null
