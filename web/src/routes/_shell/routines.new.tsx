import { createFileRoute } from "@tanstack/react-router";
import { RoutineForm } from "@/components/mr/RoutineForm";
import { usePageTitle } from "@/lib/title";

export const Route = createFileRoute("/_shell/routines/new")({
  component: NewRoutine,
});

function NewRoutine() {
  usePageTitle("New routine");
  return <RoutineForm />;
}
