# MySQL back on 10.11.9 and its old volume by hand, then its export is cut in half before the import: the run must roll back.
f=/apps/mysql/compose.yaml
sed -i -e 's|image: docker.io/library/mysql:.*|image: docker.io/library/mysql:8.4.2|' -e 's|- data-26:/var/lib/mysql$|- data:/var/lib/mysql|' -e '/^  data-26: {}$/d' "$f"
(cd /apps/mysql && docker compose up -d --wait >/dev/null 2>&1); docker volume rm mysql_data-26 >/dev/null 2>&1 || true
until docker exec -e MYSQL_PWD=palier-lab-password mysql-mysql-1 mysql -uroot -h 127.0.0.1 -e 'SELECT 1' >/dev/null 2>&1; do sleep 2; done
docker exec palier-palier-1 /palier plan mysql >/dev/null 2>&1
count() { docker exec -e MYSQL_PWD=palier-lab-password mysql-mysql-1 mysql -uroot -N -B -e "SELECT CONCAT(VERSION(), ' ; commandes ', COUNT(*)) FROM shop.orders" 2>&1; }
docker exec -e MYSQL_PWD=palier-lab-password mysql-mysql-1 mysql -uroot -e "INSERT INTO shop.customers (name) VALUES ('Hedy-$(date +%s)'); INSERT INTO shop.orders (customer_id, total) SELECT MAX(id), 42.00 FROM shop.customers"
echo "avant (une commande ajoutée hors du seed) : $(count)"
previous=$(sql "SELECT coalesce(max(id), 0) FROM runs")
(
	until sql "SELECT message FROM run_events WHERE run_id > $previous" | grep -q 'sur le nouveau volume'; do sleep 0.2; done
	dump=$(ls /var/lib/docker/volumes/palier_palier-data/_data/dumps/run*-mysql.sql | tail -1)
	size=$(stat -c %s "$dump"); truncate -s $((size / 2)) "$dump"
	echo "export coupé : $size → $((size / 2)) octets"
) &
apply_and_report mysql
wait
echo "après : $(count) ; volumes : $(docker volume ls -q | grep '^mysql_' | tr '\n' ' ')"
grep -E 'image:|/var/lib/mysql' "$f"
