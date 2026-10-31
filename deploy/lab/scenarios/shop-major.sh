# Major upgrade of MariaDB and MySQL through export and import: same customers, orders, keys and users after, old volume kept.
shop() { docker exec -e MYSQL_PWD=palier-lab-password "$1-$1-1" "$2" -uroot -N -B -e "SELECT CONCAT(VERSION(), ' ; clients ', (SELECT COUNT(*) FROM shop.customers), ' ; commandes ', (SELECT COUNT(*) FROM shop.orders), ' ; total ', (SELECT SUM(total) FROM shop.orders), ' ; contraintes ', (SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema = 'shop'), ' ; utilisateurs ', (SELECT GROUP_CONCAT(CONCAT(user, '@', host)) FROM mysql.user WHERE user = 'app'))" 2>&1 | tr '\n' ' '; }
for db in mariadb mysql; do
	client=$db; [ $db = mysql ] || client=mariadb
	echo "== $db avant : $(shop $db $client) ; volumes : $(docker volume ls -q | grep "^${db}_" | tr '\n' ' ')"
	apply_and_report $db
	echo "== $db après : $(shop $db $client) ; volumes : $(docker volume ls -q | grep "^${db}_" | tr '\n' ' ') ; montage : $(docker inspect $db-$db-1 --format '{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}}:{{.Destination}}{{end}}{{end}}')"
done
