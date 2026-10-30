user = User.find_by_username('root')
project = Project.find_by_full_path('root/lab-demo') ||
  Projects::CreateService.new(user, name: 'lab-demo', path: 'lab-demo', initialize_with_readme: true).execute
raise project.errors.full_messages.join(', ') unless project.persisted?
Issues::CreateService.new(container: project, current_user: user, params: { title: 'Upgrade GitLab' }).execute if project.issues.empty?
puts 'seeded'
