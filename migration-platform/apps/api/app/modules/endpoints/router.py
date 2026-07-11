from fastapi import APIRouter, Depends, status
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.endpoints import service
from app.modules.endpoints.schemas import CredentialUpdate, EndpointCreate, EndpointRead, EndpointUpdate

router = APIRouter(tags=["endpoints"])


@router.get("/api/migrations/{migration_id}/endpoints", response_model=list[EndpointRead])
def list_endpoints(migration_id: int, db: Session = Depends(get_db)) -> list[dict]:
    return service.list_endpoints(db, migration_id)


@router.post("/api/migrations/{migration_id}/endpoints", response_model=EndpointRead, status_code=status.HTTP_201_CREATED)
def create_endpoint(migration_id: int, payload: EndpointCreate, db: Session = Depends(get_db)) -> dict:
    return service.create_endpoint(db, migration_id, payload)


@router.patch("/api/endpoints/{endpoint_id}", response_model=EndpointRead)
def update_endpoint(endpoint_id: int, payload: EndpointUpdate, db: Session = Depends(get_db)) -> dict:
    return service.update_endpoint(db, endpoint_id, payload)


@router.patch("/api/endpoints/{endpoint_id}/credentials", response_model=EndpointRead)
def update_credentials(endpoint_id: int, payload: CredentialUpdate, db: Session = Depends(get_db)) -> dict:
    return service.update_credentials(db, endpoint_id, payload.token)


@router.post("/api/endpoints/{endpoint_id}/test-connection", response_model=EndpointRead)
def test_connection(endpoint_id: int, db: Session = Depends(get_db)) -> dict:
    return service.test_connection(db, endpoint_id)


@router.delete("/api/endpoints/{endpoint_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_endpoint(endpoint_id: int, db: Session = Depends(get_db)) -> None:
    service.delete_endpoint(db, endpoint_id)
