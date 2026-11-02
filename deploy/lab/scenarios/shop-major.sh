# Major upgrade of MariaDB and MySQL through export and import: same customers, orders, keys and users after, old volume kept.
shop() { docker exec -e MYSQL_PWD=palier-lab-password "$1-$1-1" "$2" -uroot -N -B -e "SELECT CONCAT(VERSION(), '; customers ', (SELECT COUNT(*) FROM shop.customers), '; orders ', (SELECT COUNT(*) FROM shop.orders), '; total ', (SELECT SUM(total) FROM shop.orders), '; constraints ', (SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema = 'shop'), '; users ', (SELECT GROUP_CONCAT(CONCAT(user, '@', host)) FROM mysql.user WHERE user = 'app'))" 2>&1 | tr '\n' ' '; }
for db in mariadb mysql; do
	client=$db; [ $db = mysql ] || client=mariadb
	echo "== $db before: $(shop $db $client); volumes: $(docker volume ls -q | grep "^${db}_" | tr '\n' ' ')"
	apply_and_report $db
	echo "== $db after: $(shop $db $client); volumes: $(docker volume ls -q | grep "^${db}_" | tr '\n' ' ') ; montage : $(docker inspect $db-$db-1 --format '{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}}:{{.Destination}}{{end}}{{end}}')"
done
