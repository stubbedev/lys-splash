start_all()

with logs_on_failure(machine, greeter_logs):
    await_gate(machine)
    hand_over(machine)
