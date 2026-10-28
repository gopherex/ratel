-- name: MergeUser :one
MERGE INTO public.users u USING public.users m ON u.id=m.id
WHEN MATCHED THEN UPDATE SET name=$1 RETURNING u.id;

-- name: MergeMissingSource :many
MERGE INTO public.users u USING (SELECT id FROM public.users WHERE false) m ON u.id=m.id
WHEN NOT MATCHED BY SOURCE THEN DELETE RETURNING m.id;
