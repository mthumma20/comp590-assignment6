defmodule SleepingBarber do
  defmodule Config do
    @total_customers 20
    @waiting_room_capacity 5
    @arrival_min_ms 500
    @arrival_max_ms 2000
    @haircut_min_ms 1000
    @haircut_max_ms 4000
    @satisfaction_wait_threshold_sec 3.0
    @shutdown_grace_ms 8000

    def total_customers, do: @total_customers
    def waiting_room_capacity, do: @waiting_room_capacity
    def arrival_min_ms, do: @arrival_min_ms
    def arrival_max_ms, do: @arrival_max_ms
    def haircut_min_ms, do: @haircut_min_ms
    def haircut_max_ms, do: @haircut_max_ms
    def satisfaction_wait_threshold_sec, do: @satisfaction_wait_threshold_sec
    def shutdown_grace_ms, do: @shutdown_grace_ms
  end

  defmodule Log do
    def now_ms, do: System.monotonic_time(:millisecond)

    def log(start_ms, who, msg) do
      elapsed = now_ms() - start_ms
      IO.puts("[#{elapsed} ms] #{who}: #{msg}")
    end
  end

  defmodule Util do
    def rand_between(min, max) when min <= max do
      :rand.uniform(max - min + 1) + min - 1
    end

    def clamp(val, low, high) do
      max(low, min(high, val))
    end

    def avg_add(old_avg, count_before, new_val) do
      ((old_avg * count_before) + new_val) / (count_before + 1)
    end
  end

  def customer(id, waiting_room, start_ms) do
    arrival_ms = Log.now_ms()
    Log.log(start_ms, "Customer #{id}", "arrives")
    send(waiting_room, {:arrive, self(), id, arrival_ms})

    receive do
      {:turned_away} ->
        Log.log(start_ms, "Customer #{id}", "turned away and exits")

      {:admitted, queue_depth} ->
        Log.log(start_ms, "Customer #{id}", "admitted to waiting room, queue depth #{queue_depth}")
        wait_for_barber(id, arrival_ms, start_ms)
    end
  end

  defp wait_for_barber(id, arrival_ms, start_ms) do
    receive do
      {:called_by_barber, barber_pid} ->
        haircut_start_ms = Log.now_ms()
        wait_ms = haircut_start_ms - arrival_ms
        Log.log(start_ms, "Customer #{id}", "called by barber after waiting #{wait_ms} ms")

        receive do
          {:rate_request, ^barber_pid} ->
            threshold_ms = trunc(Config.satisfaction_wait_threshold_sec() * 1000)
            stars_lost = div(wait_ms, threshold_ms)
            jitter = Enum.random(-1..1)
            rating = Util.clamp(5 - stars_lost + jitter, 1, 5)
            Log.log(start_ms, "Customer #{id}", "gives rating #{rating} after waiting #{wait_ms} ms")
            send(barber_pid, {:rating, self(), id, rating})
        end
    end
  end

  def waiting_room(barber_pid, owner_pid, start_ms) do
    state = %{
      queue: :queue.new(),
      queue_len: 0,
      capacity: Config.waiting_room_capacity(),
      turned_away: 0,
      barber_sleeping: false,
      barber_pid: barber_pid,
      owner_pid: owner_pid
    }

    waiting_room_loop(state, start_ms)
  end

  defp waiting_room_loop(state, start_ms) do
    receive do
      {:arrive, customer_pid, customer_id, _arrival_ms} ->
        if state.queue_len >= state.capacity do
          send(customer_pid, {:turned_away})
          new_state = %{state | turned_away: state.turned_away + 1}
          Log.log(start_ms, "WaitingRoom", "turns away Customer #{customer_id}; queue depth #{state.queue_len}")
          waiting_room_loop(new_state, start_ms)
        else
          new_queue = :queue.in({customer_pid, customer_id}, state.queue)
          new_len = state.queue_len + 1
          send(customer_pid, {:admitted, new_len})
          Log.log(start_ms, "WaitingRoom", "admits Customer #{customer_id}; queue depth #{new_len}")

          {state2, woke?} =
            if state.barber_sleeping do
              send(state.barber_pid, {:wakeup})
              Log.log(start_ms, "WaitingRoom", "wakes barber")
              {%{state | queue: new_queue, queue_len: new_len, barber_sleeping: false}, true}
            else
              {%{state | queue: new_queue, queue_len: new_len}, false}
            end

          _ = woke?
          waiting_room_loop(state2, start_ms)
        end

      {:next_customer, barber_pid} ->
        if state.queue_len > 0 do
          {{:value, {customer_pid, customer_id}}, new_queue} = :queue.out(state.queue)
          send(barber_pid, {:customer_ready, customer_pid, customer_id})
          Log.log(start_ms, "WaitingRoom", "sends Customer #{customer_id} to barber")
          waiting_room_loop(%{state | queue: new_queue, queue_len: state.queue_len - 1, barber_sleeping: false}, start_ms)
        else
          send(barber_pid, {:none_waiting})
          Log.log(start_ms, "WaitingRoom", "no customers waiting; barber should sleep")
          waiting_room_loop(%{state | barber_sleeping: true}, start_ms)
        end

      {:get_stats, requester} ->
        send(requester, {:stats_reply, :waiting_room, %{turned_away: state.turned_away, queue_length: state.queue_len}})
        waiting_room_loop(state, start_ms)

      {:shutdown} ->
        Log.log(start_ms, "WaitingRoom", "shutdown received; exiting")
        :ok
    end
  end

  def barber(waiting_room_pid, start_ms) do
    state = %{
      haircuts_completed: 0,
      avg_duration_ms: 0.0,
      avg_rating: 0.0
    }

    Log.log(start_ms, "Barber", "starts up")
    send(waiting_room_pid, {:next_customer, self()})
    barber_loop(state, waiting_room_pid, start_ms)
  end

  defp barber_loop(state, waiting_room_pid, start_ms) do
    receive do
      {:none_waiting} ->
        Log.log(start_ms, "Barber", "goes to sleep")
        sleep_loop(state, waiting_room_pid, start_ms)

      {:customer_ready, customer_pid, customer_id} ->
        haircut_duration = Util.rand_between(Config.haircut_min_ms(), Config.haircut_max_ms())
        Log.log(start_ms, "Barber", "starts haircut for Customer #{customer_id}; planned duration #{haircut_duration} ms")
        send(customer_pid, {:called_by_barber, self()})
        :timer.sleep(haircut_duration)
        Log.log(start_ms, "Barber", "finishes haircut for Customer #{customer_id}; asks for rating")
        send(customer_pid, {:rate_request, self()})

        receive do
          {:rating, ^customer_pid, ^customer_id, rating} ->
            completed = state.haircuts_completed
            new_avg_dur = Util.avg_add(state.avg_duration_ms, completed, haircut_duration)
            new_avg_rating = Util.avg_add(state.avg_rating, completed, rating)

            new_state = %{
              haircuts_completed: completed + 1,
              avg_duration_ms: new_avg_dur,
              avg_rating: new_avg_rating
            }

            Log.log(
              start_ms,
              "Barber",
              "received rating #{rating}; cuts=#{new_state.haircuts_completed}, avg_duration=#{Float.round(new_avg_dur / 1000, 2)}s, avg_rating=#{Float.round(new_avg_rating, 2)}"
            )

            send(waiting_room_pid, {:next_customer, self()})
            barber_loop(new_state, waiting_room_pid, start_ms)
        end

      {:get_stats, requester} ->
        send(requester, {:stats_reply, :barber, state})
        barber_loop(state, waiting_room_pid, start_ms)

      {:shutdown} ->
        Log.log(start_ms, "Barber", "shutdown received; exiting")
        :ok
    end
  end

  defp sleep_loop(state, waiting_room_pid, start_ms) do
    receive do
      {:wakeup} ->
        Log.log(start_ms, "Barber", "wakes up")
        send(waiting_room_pid, {:next_customer, self()})
        barber_loop(state, waiting_room_pid, start_ms)

      {:get_stats, requester} ->
        send(requester, {:stats_reply, :barber, state})
        sleep_loop(state, waiting_room_pid, start_ms)

      {:shutdown} ->
        Log.log(start_ms, "Barber", "shutdown received while sleeping; exiting")
        :ok

      {:customer_ready, customer_pid, customer_id} ->
        # harmless if already queued in mailbox
        send(self(), {:customer_ready, customer_pid, customer_id})
        barber_loop(state, waiting_room_pid, start_ms)

      {:none_waiting} ->
        sleep_loop(state, waiting_room_pid, start_ms)
    end
  end

  def shop_owner(start_ms) do
    parent = self()

    barber_pid =
      spawn(fn ->
        receive do
          {:init_waiting_room, waiting_room_pid} ->
            barber(waiting_room_pid, start_ms)
        end
      end)

    waiting_room_pid =
      spawn(fn ->
        waiting_room(barber_pid, parent, start_ms)
      end)

    send(barber_pid, {:init_waiting_room, waiting_room_pid})

    total = Config.total_customers()

    Enum.each(1..total, fn id ->
      Log.log(start_ms, "ShopOwner", "spawns Customer #{id} of #{total}")

      spawn(fn ->
        customer(id, waiting_room_pid, start_ms)
      end)

      if id < total do
        :timer.sleep(Util.rand_between(Config.arrival_min_ms(), Config.arrival_max_ms()))
      end
    end)

    Log.log(start_ms, "ShopOwner", "all customers spawned; waiting grace period #{Config.shutdown_grace_ms()} ms")
    :timer.sleep(Config.shutdown_grace_ms())

    send(barber_pid, {:get_stats, self()})
    send(waiting_room_pid, {:get_stats, self()})

    barber_stats =
      receive do
        {:stats_reply, :barber, stats} -> stats
      end

    wr_stats =
      receive do
        {:stats_reply, :waiting_room, stats} -> stats
      end

    send(barber_pid, {:shutdown})
    send(waiting_room_pid, {:shutdown})

    Log.log(start_ms, "ShopOwner", "shutdown initiated")
    :timer.sleep(200)

    print_report(total, barber_stats, wr_stats)
  end

  defp print_report(total_customers, barber_stats, wr_stats) do
    avg_sec = barber_stats.avg_duration_ms / 1000.0

    IO.puts("")
    IO.puts("=== Barbershop Closing Report ===")
    IO.puts("Total customers arrived: #{total_customers}")
    IO.puts("Customers served: #{barber_stats.haircuts_completed}")
    IO.puts("Customers turned away: #{wr_stats.turned_away}")
    IO.puts("Average haircut duration: #{Float.round(avg_sec, 2)}s")
    IO.puts("Average satisfaction: #{Float.round(barber_stats.avg_rating, 2)} / 5.0")
    IO.puts("=================================")
  end

  def main do
    :rand.seed(:exsss, {System.unique_integer(), System.unique_integer(), System.unique_integer()})
    start_ms = Log.now_ms()
    shop_owner(start_ms)
  end
end

SleepingBarber.main()