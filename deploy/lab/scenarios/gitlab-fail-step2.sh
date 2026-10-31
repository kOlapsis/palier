# The image of the second stop of the GitLab plan exits at once: GitLab must come back to its starting version with its data.
fake_step_image gitlab 1
echo "avant :"; gitlab_state
start=$(date +%s)
apply_and_report gitlab
echo "durée : $(( $(date +%s) - start )) s"
echo "après :"; gitlab_state
grep image: /apps/gitlab/compose.yaml
docker rmi "$FAKE" >/dev/null
