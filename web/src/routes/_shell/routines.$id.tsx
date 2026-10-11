import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/mr/common";
import { RoutineForm } from "@/components/mr/RoutineForm";
import { q } from "@/lib/queries";
import { usePageTitle } from "@/lib/title";

export const Route = createFileRoute("/_shell/routines/$id")({
  component: EditRoutine,
});

function EditRoutine() {
  usePageTitle("Edit routine");
  const { id } = Route.useParams();
  const { data, isLoading, error, refetch } = useQuery(q.routine(id));
  if (error) return <ErrorState message={(error as Error).message} onRetry={() => refetch()} />;
  if (isLoading || !data)
    return (
      <div className="space-y-6">
        <Skeleton className="h-8 w-60" />
        {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-40 w-full" />)}
      </div>
    );
  return <RoutineForm key={data.id} routine={data} />;
}
